import org.gradle.api.tasks.PathSensitivity
import org.gradle.api.tasks.Sync
import org.jetbrains.kotlin.gradle.targets.js.yarn.YarnPlugin
import org.jetbrains.kotlin.gradle.targets.js.yarn.YarnRootExtension

plugins {
    // this is necessary to avoid the plugins to be loaded multiple times
    // in each subproject's classloader
    alias(libs.plugins.androidApplication) apply false
    alias(libs.plugins.androidMultiplatformLibrary) apply false
    alias(libs.plugins.composeMultiplatform) apply false
    alias(libs.plugins.composeCompiler) apply false
    alias(libs.plugins.kotlinMultiplatform) apply false
    alias(libs.plugins.kotlinSerialization) apply false
}

/**
 * Keeps the Kotlin/JS toolchain's yarn store inside the build directory.
 *
 * The plugin defaults it to `<root>/kotlin-js-store`, which is neither a build
 * directory nor part of the WeChat deliverable, so it would sit in the working
 * tree as an untracked artefact. This project has no npm runtime dependencies —
 * the store only pins the TypeScript version used to run the MiniApp tests — so
 * relocating it under `build/` leaves every generated file in a directory that
 * is already ignored by `clean` and by `.gitignore`.
 */
rootProject.plugins.withType(YarnPlugin::class.java) {
    rootProject.the<YarnRootExtension>().lockFileDirectoryProperty.set(
        rootProject.layout.buildDirectory.dir("kotlin-js-store"),
    )
}

/**
 * Copies the plugin-owned CommonJS distribution into the WeChat host directory.
 *
 * The Compose Multiplatform plugin also emits browser/WASM renderer assets
 * (`skiko.wasm`, `skiko*.mjs`), ES-module re-export shims and source maps into
 * the same bundle directory. Nothing in the WeChat runtime loads them — the
 * entry file requires only the Kotlin stdlib, serialization and the MiniApp SDK
 * — and the WeChat main package limit is 2 MB, so shipping them would both
 * contradict the no-Compose boundary and make the package un-uploadable.
 */
val miniAppDir = layout.projectDirectory.dir("miniApp")

val prepareMiniAppHost by tasks.registering(Sync::class) {
    dependsOn(":shared:assembleMiniAppBundle")
    from(project(":shared").layout.buildDirectory.dir("miniapp/bundle")) {
        exclude("skiko*.mjs", "skiko.wasm", "js-reexport-symbols.mjs")
        exclude("*.map")
        exclude("composeResources/**")
    }
    into(miniAppDir.dir("kotlin"))
}

/**
 * Verifies that `miniApp/` is the only thing WeChat needs.
 *
 * Declared as its own task rather than a `doLast` on the copy so that editing
 * the host scripts re-runs it: a `doLast` on an up-to-date `Sync` never fires,
 * which would let a bad `require` through until the next clean build.
 *
 * It fails when:
 *  - a Compose/browser asset reached the copied bundle,
 *  - the CommonJS entry file is missing, or
 *  - a relative `require` resolves outside `miniApp/`.
 *
 * The last case is the subtle one: such a project works locally and breaks the
 * moment the folder is copied to another machine, uploaded, or built from a
 * clean checkout.
 */
val checkMiniAppHostSelfContained by tasks.registering {
    group = "verification"
    description = "Fails when the WeChat folder is not self-contained or carries Compose assets"
    dependsOn(prepareMiniAppHost)

    val wechatProject = miniAppDir.asFileTree
    inputs.files(wechatProject)
        .withPropertyName("wechatProject")
        .withPathSensitivity(PathSensitivity.RELATIVE)

    doLast {
        val root = miniAppDir.asFile.canonicalFile
        val bundleDir = miniAppDir.dir("kotlin").asFile

        val files = bundleDir.walkTopDown().filter { it.isFile }.toList()
        val forbidden = listOf("skiko.wasm", "js-reexport-symbols.mjs", "composeResources")
        val leaked = forbidden.filter { name -> files.any { it.name == name } }
        check(leaked.isEmpty()) {
            "the WeChat bundle still contains Compose/browser assets: $leaked"
        }
        check(files.any { it.name == "client-kmp-shared-miniapp.js" }) {
            "the WeChat bundle is missing the CommonJS entry file client-kmp-shared-miniapp.js;" +
                " run `./gradlew prepareMiniAppHost`"
        }

        val requirePattern = Regex("""require\(\s*['"](\.[^'"]*)['"]\s*\)""")
        val escapees = wechatProject.files
            .filter { it.isFile && it.extension == "js" }
            .flatMap { file ->
                requirePattern.findAll(file.readText()).mapNotNull { match ->
                    val target = File(file.parentFile, match.groupValues[1]).canonicalFile
                    if (target.startsWith(root)) {
                        null
                    } else {
                        "${file.relativeTo(root)} requires '${match.groupValues[1]}' outside miniApp/"
                    }
                }
            }
        check(escapees.isEmpty()) {
            "the WeChat project is not self-contained:\n  " + escapees.joinToString("\n  ")
        }

        // A 202 response and an exhausted acknowledgement poll are not device
        // success. Keep the threshold control flow tied to the shared `confirmed`
        // flag so pending/duplicate/failed outcomes never render a green tick.
        val monitorScript = miniAppDir.file("pages/monitor/monitor.js").asFile.readText()
        val confirmedToast = "icon: outcome && outcome.confirmed ? 'success' : 'none'"
        check(Regex(Regex.escape(confirmedToast)).findAll(monitorScript).count() == 1) {
            "the MiniApp threshold control flow must show success only for a confirmed device acknowledgement"
        }

        logger.lifecycle("miniApp/ is self-contained: ${files.size} bundle files, no external requires")
    }
}

// `:shared` owns the bundle being copied, so its `check` is where the WeChat
// deliverable gets verified. Wired lazily: the root script is evaluated before
// `:shared` exists, and the root project itself has no `check` task.
project(":shared").tasks.matching { it.name == "check" }.configureEach {
    dependsOn(checkMiniAppHostSelfContained)
}
