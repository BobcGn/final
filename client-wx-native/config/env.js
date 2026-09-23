/**
 * 环境配置（唯一修改点）。
 *
 * useMock: true  -> 所有请求走本地 Mock（services/mock/），仅用于离线开发。
 * useMock: false -> 默认请求真实后端，地址取 baseUrl / wsUrl。
 *
 * 联调提示：微信开发者工具需勾选「不校验合法域名」；真机调试时
 * baseUrl 改成运行后端电脑的局域网 IP（手机上的 localhost 指向手机本身）。
 */
const env = {
  useMock: false,
  baseUrl: 'http://localhost:8080',
  wsUrl: 'ws://localhost:8080',
  requestTimeout: 5000,
}

module.exports = { env }
