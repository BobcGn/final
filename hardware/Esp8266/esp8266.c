#include "esp8266.h"

#define ESP8266_RETRY_CYCLES 5U
#define ESP8266_AT_RETRY_COUNT 3U
#define ESP8266_AT_TIMEOUT_MS 2000U

#define ESP8266_RX_BUFFER_SIZE 512U
#define ESP8266_COMMAND_BUFFER_SIZE 128U
#define ESP8266_SEND_BUFFER_SIZE 96U
#define ESP8266_MESSAGE_BUFFER_SIZE 640U

static volatile char s_rxBuffer[ESP8266_RX_BUFFER_SIZE];
static volatile uint16_t s_rxIndex = 0U;
static volatile char s_receivedCommand = 0;
static volatile uint8_t s_messageBuffer[ESP8266_MESSAGE_BUFFER_SIZE];
static volatile uint16_t s_messageLength = 0U;
static volatile uint16_t s_ipdPayloadRemaining = 0U;
static volatile uint8_t s_messageReady = 0U;
static volatile uint8_t s_ipdState = 0U;
/* Set while the payload of the current +IPD frame is being consumed without
 * being stored, because an earlier frame has not been collected yet. */
static volatile uint8_t s_messageDiscard = 0U;
/* Receive frames the driver could not hand up. They are counted so that a lost
 * control command is visible instead of looking like a broker that sent nothing;
 * device/control is QoS 1, so the broker's redelivery is what recovers them. */
static volatile uint32_t s_discardedFrames = 0U;
static volatile uint32_t s_truncatedFrames = 0U;
static volatile uint8_t s_wifiConnected = 0U;
static volatile uint8_t s_tcpConnected = 0U;
static volatile uint8_t s_registered = 0U;
static volatile uint8_t s_wifiGotIpIndex = 0U;
static volatile uint8_t s_wifiDisconnectIndex = 0U;
static volatile uint8_t s_tcpClosedIndex = 0U;

static const char s_wifiGotIpText[] = "WIFI GOT IP";
static const char s_wifiDisconnectText[] = "WIFI DISCONNECT";
static const char s_tcpClosedText[] = "CLOSED";

static uint16_t s_gasPpm = 0U;
static uint8_t s_humidity = 0U;
static uint8_t s_temperature = 0U;

static uint8_t ESP8266_MatchStatusText(char received, const char *text,
                                       volatile uint8_t *index) {
  if (received == text[*index]) {
    (*index)++;
  } else {
    *index = (received == text[0]) ? 1U : 0U;
  }

  if (text[*index] == '\0') {
    *index = 0U;
    return 1U;
  }

  return 0U;
}

static void ESP8266_ParseIncomingByte(char received) {
  /* +IPD 的正文是用户数据，不能把其中的文本当成模块状态提示。 */
  if ((s_ipdState != 6U) &&
      (ESP8266_MatchStatusText(received, s_wifiGotIpText, &s_wifiGotIpIndex) !=
       0U)) {
    s_wifiConnected = 1U;
  }

  if ((s_ipdState != 6U) &&
      (ESP8266_MatchStatusText(received, s_tcpClosedText, &s_tcpClosedIndex) !=
       0U)) {
    s_tcpConnected = 0U;
    s_registered = 0U;
  }

  if ((s_ipdState != 6U) &&
      (ESP8266_MatchStatusText(received, s_wifiDisconnectText,
                               &s_wifiDisconnectIndex) != 0U)) {
    s_wifiConnected = 0U;
    s_tcpConnected = 0U;
    s_registered = 0U;
  }

  switch (s_ipdState) {
  case 0U:
    if (received == '+')
      s_ipdState = 1U;
    break;
  case 1U:
    s_ipdState = (received == 'I') ? 2U : 0U;
    break;
  case 2U:
    s_ipdState = (received == 'P') ? 3U : 0U;
    break;
  case 3U:
    s_ipdState = (received == 'D') ? 4U : 0U;
    break;
  case 4U:
    if (received == ',') {
      s_ipdPayloadRemaining = 0U;
      s_ipdState = 5U;
    } else {
      s_ipdState = 0U;
    }
    break;
  case 5U:
    if ((received >= '0') && (received <= '9')) {
      s_ipdPayloadRemaining =
          (uint16_t)(s_ipdPayloadRemaining * 10U + (uint16_t)(received - '0'));
    } else if ((received == ':') && (s_ipdPayloadRemaining > 0U)) {
      if (s_messageReady != 0U) {
        /* A previous frame is still waiting to be collected. Starting this one
         * in the same buffer would overwrite it without a trace, so this frame's
         * payload is consumed and dropped instead and the loss is counted. The
         * older frame is kept because it was delivered first: it may be the
         * CONNACK or SUBACK the session is waiting on, and a command it carries
         * is lost only until the broker redelivers at QoS 1. */
        s_messageDiscard = 1U;
        s_discardedFrames++;
      } else {
        s_messageDiscard = 0U;
        s_messageLength = 0U;
        s_wifiGotIpIndex = 0U;
        s_wifiDisconnectIndex = 0U;
      }
      s_ipdState = 6U;
    } else {
      s_ipdState = 0U;
    }
    break;
  case 6U:
    if (s_messageDiscard == 0U) {
      if (s_messageLength < ESP8266_MESSAGE_BUFFER_SIZE) {
        s_messageBuffer[s_messageLength] = (uint8_t)received;
        s_messageLength++;
      } else if (s_truncatedFrames == 0U) {
        /* The payload is longer than the buffer, so what is stored is a prefix
         * that will not parse. Counted once per frame rather than once per byte. */
        s_truncatedFrames++;
      }
    }

    s_ipdPayloadRemaining--;
    if (s_ipdPayloadRemaining == 0U) {
      if (s_messageDiscard == 0U) {
        s_messageReady = 1U;
      }
      s_messageDiscard = 0U;
      s_ipdState = 0U;
    }
    break;
  default:
    s_ipdState = 0U;
    break;
  }
}

void USART1_IRQHandler(void) {
  if (USART_GetITStatus(ESP8266_USART, USART_IT_RXNE) != RESET) {
    char received = (char)(USART_ReceiveData(ESP8266_USART) & 0xFFU);

    ESP8266_ParseIncomingByte(received);

    if ((received == 'a') || (received == 'b')) {
      s_receivedCommand = received;
    }

    if (s_rxIndex < (ESP8266_RX_BUFFER_SIZE - 1U)) {
      s_rxBuffer[s_rxIndex] = received;
      s_rxIndex++;
      s_rxBuffer[s_rxIndex] = '\0';
    }
  }
}

static void ESP8266_ClearRxBuffer(void) {
  USART_ITConfig(ESP8266_USART, USART_IT_RXNE, DISABLE);
  s_rxIndex = 0U;
  s_rxBuffer[0] = '\0';
  USART_ITConfig(ESP8266_USART, USART_IT_RXNE, ENABLE);
}

static uint8_t ESP8266_BufferContains(const char *target) {
  uint16_t start;

  if ((target == 0) || (target[0] == '\0')) {
    return 0U;
  }

  for (start = 0U; start < s_rxIndex; start++) {
    uint16_t offset = 0U;

    while ((target[offset] != '\0') && ((start + offset) < s_rxIndex) &&
           (s_rxBuffer[start + offset] == target[offset])) {
      offset++;
    }

    if (target[offset] == '\0') {
      return 1U;
    }
  }

  return 0U;
}

static uint8_t ESP8266_WaitFor(const char *expected, const char *alternative,
                               uint32_t timeoutMs) {
  uint32_t elapsed = 0U;

  while (elapsed < timeoutMs) {
    if ((ESP8266_BufferContains(expected) != 0U) ||
        ((alternative != 0) && (ESP8266_BufferContains(alternative) != 0U))) {
      return 1U;
    }

    if ((ESP8266_BufferContains("ERROR") != 0U) ||
        (ESP8266_BufferContains("FAIL") != 0U)) {
      return 0U;
    }

    delay_ms(10U);
    elapsed += 10U;
  }

  return 0U;
}

static void ESP8266_SendByte(uint8_t data) {
  while (USART_GetFlagStatus(ESP8266_USART, USART_FLAG_TXE) == RESET) {
  }
  USART_SendData(ESP8266_USART, data);
}

static void ESP8266_SendString(const char *text) {
  while (*text != '\0') {
    ESP8266_SendByte((uint8_t)*text);
    text++;
  }
}

static void ESP8266_SendCommand(const char *command) {
  ESP8266_ClearRxBuffer();
  ESP8266_SendString(command);
  ESP8266_SendString("\r\n");
}

static uint8_t ESP8266_SyncAt(void) {
  uint8_t attempt;

  for (attempt = 0U; attempt < ESP8266_AT_RETRY_COUNT; attempt++) {
    ESP8266_SendCommand("AT");
    if (ESP8266_WaitFor("OK", 0, ESP8266_AT_TIMEOUT_MS) != 0U) {
      return 1U;
    }
    delay_ms(300U);
  }

  return 0U;
}

static uint8_t ESP8266_AppendChar(char *buffer, uint16_t capacity,
                                  uint16_t *length, char value) {
  if ((*length + 1U) >= capacity) {
    return 0U;
  }

  buffer[*length] = value;
  (*length)++;
  buffer[*length] = '\0';
  return 1U;
}

static uint8_t ESP8266_AppendString(char *buffer, uint16_t capacity,
                                    uint16_t *length, const char *text) {
  while (*text != '\0') {
    if (ESP8266_AppendChar(buffer, capacity, length, *text) == 0U) {
      return 0U;
    }
    text++;
  }

  return 1U;
}

static uint8_t ESP8266_AppendUnsigned(char *buffer, uint16_t capacity,
                                      uint16_t *length, uint16_t value) {
  char digits[5];
  uint8_t count = 0U;

  do {
    digits[count] = (char)('0' + (value % 10U));
    value /= 10U;
    count++;
  } while ((value != 0U) && (count < sizeof(digits)));

  while (count > 0U) {
    count--;
    if (ESP8266_AppendChar(buffer, capacity, length, digits[count]) == 0U) {
      return 0U;
    }
  }

  return 1U;
}

static uint16_t ESP8266_StringLength(const char *text) {
  uint16_t length = 0U;

  while (text[length] != '\0') {
    length++;
  }

  return length;
}

uint8_t ESP8266_SendBytes(const uint8_t *payload, uint16_t payloadLength) {
  char command[ESP8266_COMMAND_BUFFER_SIZE];
  uint16_t length = 0U;

  if ((payload == 0) || (payloadLength == 0U)) {
    return 0U;
  }

  command[0] = '\0';
  if ((ESP8266_AppendString(command, sizeof(command), &length, "AT+CIPSEND=") ==
       0U) ||
      (ESP8266_AppendUnsigned(command, sizeof(command), &length,
                              payloadLength) == 0U)) {
    return 0U;
  }

  ESP8266_SendCommand(command);
  if (ESP8266_WaitFor(">", 0, 2000U) == 0U) {
    return 0U;
  }

  ESP8266_ClearRxBuffer();
  {
    uint16_t index;
    for (index = 0U; index < payloadLength; index++) {
      ESP8266_SendByte(payload[index]);
    }
  }
  return ESP8266_WaitFor("SEND OK", 0, 3000U);
}

static uint8_t ESP8266_SendPayload(const char *payload) {
  return ESP8266_SendBytes((const uint8_t *)payload,
                           ESP8266_StringLength(payload));
}

static void ESP8266_UartInit(void) {
  GPIO_InitTypeDef gpio;
  USART_InitTypeDef usart;
  NVIC_InitTypeDef nvic;

  RCC_APB2PeriphClockCmd(RCC_APB2Periph_USART1 | RCC_APB2Periph_GPIOA, ENABLE);

  gpio.GPIO_Pin = ESP8266_TX_PIN;
  gpio.GPIO_Mode = GPIO_Mode_AF_PP;
  gpio.GPIO_Speed = GPIO_Speed_50MHz;
  GPIO_Init(ESP8266_GPIO_PORT, &gpio);

  gpio.GPIO_Pin = ESP8266_RX_PIN;
  gpio.GPIO_Mode = GPIO_Mode_IN_FLOATING;
  GPIO_Init(ESP8266_GPIO_PORT, &gpio);

  USART_StructInit(&usart);
  usart.USART_BaudRate = ESP8266_BAUD_RATE;
  usart.USART_Mode = USART_Mode_Tx | USART_Mode_Rx;
  USART_Init(ESP8266_USART, &usart);

  nvic.NVIC_IRQChannel = USART1_IRQn;
  nvic.NVIC_IRQChannelPreemptionPriority = 1U;
  nvic.NVIC_IRQChannelSubPriority = 0U;
  nvic.NVIC_IRQChannelCmd = ENABLE;
  NVIC_Init(&nvic);

  USART_ITConfig(ESP8266_USART, USART_IT_RXNE, ENABLE);
  USART_Cmd(ESP8266_USART, ENABLE);
}

uint8_t ESP8266_Init(void) {
  char command[ESP8266_COMMAND_BUFFER_SIZE];
  uint16_t length = 0U;

  s_wifiConnected = 0U;
  s_tcpConnected = 0U;
  s_registered = 0U;

  ESP8266_UartInit();
  delay_ms(2000U);

  /* 先确认主控与 AT 端口通信正常，再执行模块复位。 */
  if (ESP8266_SyncAt() == 0U) {
    return ESP8266_STATUS_AT_ERROR;
  }

  ESP8266_SendCommand("AT+RST");
  /* 官方应答为 OK；部分固件还会在启动完成后输出 ready。 */
  if (ESP8266_WaitFor("OK", "ready", 3000U) == 0U) {
    return ESP8266_STATUS_AT_ERROR;
  }
  delay_ms(2000U);

  /* 复位完成后再次同步，避免把复位前的应答当成当前状态。 */
  if (ESP8266_SyncAt() == 0U) {
    return ESP8266_STATUS_AT_ERROR;
  }

  ESP8266_SendCommand("ATE0");
  if (ESP8266_WaitFor("OK", 0, ESP8266_AT_TIMEOUT_MS) == 0U) {
    return ESP8266_STATUS_AT_ERROR;
  }

  ESP8266_SendCommand("AT+CWMODE=1");
  if (ESP8266_WaitFor("OK", "no change", 3000U) == 0U) {
    /* 兼容使用旧版 NONOS-AT 固件的 ESP-01/ESP-01S。 */
    ESP8266_SendCommand("AT+CWMODE_CUR=1");
    if (ESP8266_WaitFor("OK", "no change", 3000U) == 0U) {
      return ESP8266_STATUS_AT_ERROR;
    }
  }

  command[0] = '\0';
  (void)ESP8266_AppendString(command, sizeof(command), &length, "AT+CWJAP=\"");
  (void)ESP8266_AppendString(command, sizeof(command), &length, WIFI_SSID);
  (void)ESP8266_AppendString(command, sizeof(command), &length, "\",\"");
  (void)ESP8266_AppendString(command, sizeof(command), &length, WIFI_PASSWORD);
  (void)ESP8266_AppendChar(command, sizeof(command), &length, '"');
  ESP8266_SendCommand(command);
  if (ESP8266_WaitFor("OK", 0, 30000U) == 0U) {
    if (ESP8266_BufferContains("+CWJAP:1") != 0U) {
      return ESP8266_STATUS_JOIN_TIMEOUT;
    }
    if (ESP8266_BufferContains("+CWJAP:2") != 0U) {
      return ESP8266_STATUS_WRONG_PASSWORD;
    }
    if (ESP8266_BufferContains("+CWJAP:3") != 0U) {
      return ESP8266_STATUS_AP_NOT_FOUND;
    }
    if (ESP8266_BufferContains("+CWJAP:4") != 0U) {
      return ESP8266_STATUS_JOIN_FAILED;
    }
    return ESP8266_STATUS_WIFI_ERROR;
  }
  s_wifiConnected = 1U;

  ESP8266_SendCommand("AT+CIPMUX=0");
  if (ESP8266_WaitFor("OK", 0, ESP8266_AT_TIMEOUT_MS) == 0U) {
    return ESP8266_STATUS_AT_ERROR;
  }

  return ESP8266_STATUS_OK;
}

uint8_t ESP8266_IsWifiConnected(void) { return s_wifiConnected; }

uint8_t ESP8266_IsTcpConnected(void) { return s_tcpConnected; }

uint8_t ESP8266_OpenTcp(void) {
  char command[ESP8266_COMMAND_BUFFER_SIZE];
  uint16_t length = 0U;

  if (s_wifiConnected == 0U) {
    return 0U;
  }
  if (s_tcpConnected != 0U) {
    return 1U;
  }

  command[0] = '\0';
  (void)ESP8266_AppendString(command, sizeof(command), &length,
                             "AT+CIPSTART=\"TCP\",\"");
  (void)ESP8266_AppendString(command, sizeof(command), &length, SERVER_IP);
  (void)ESP8266_AppendString(command, sizeof(command), &length, "\",");
  (void)ESP8266_AppendString(command, sizeof(command), &length, SERVER_PORT);
  ESP8266_SendCommand(command);
  if (ESP8266_WaitFor("CONNECT", "ALREADY CONNECTED", 5000U) == 0U) {
    return 0U;
  }
  s_tcpConnected = 1U;
  return 1U;
}

uint8_t ESP8266_GetPacket(uint8_t *buffer, uint16_t capacity,
                          uint16_t *length) {
  uint16_t index;

  if ((buffer == 0) || (length == 0) || (s_messageReady == 0U) ||
      (capacity < s_messageLength)) {
    return 0U;
  }
  USART_ITConfig(ESP8266_USART, USART_IT_RXNE, DISABLE);
  for (index = 0U; index < s_messageLength; index++) {
    buffer[index] = s_messageBuffer[index];
  }
  *length = s_messageLength;
  s_messageReady = 0U;
  USART_ITConfig(ESP8266_USART, USART_IT_RXNE, ENABLE);
  return 1U;
}

void ESP8266_GetReceiveStats(ESP8266ReceiveStats *stats) {
  if (stats == 0) {
    return;
  }
  stats->discarded_frames = s_discardedFrames;
  stats->truncated_frames = s_truncatedFrames;
}

void ESP8266_SetData(uint16_t gasPpm, uint8_t temperature, uint8_t humidity) {
  s_gasPpm = gasPpm;
  s_temperature = temperature;
  s_humidity = humidity;
}

char ESP8266_GetCmd(void) {
  char command = s_receivedCommand;
  s_receivedCommand = 0;
  return command;
}

uint8_t ESP8266_GetMessage(char *buffer, uint16_t capacity) {
  uint16_t index;

  if ((buffer == 0) || (capacity == 0U) || (s_messageReady == 0U)) {
    return 0U;
  }

  USART_ITConfig(ESP8266_USART, USART_IT_RXNE, DISABLE);
  for (index = 0U; (index < s_messageLength) && ((index + 1U) < capacity);
       index++) {
    buffer[index] = (char)s_messageBuffer[index];
  }
  buffer[index] = '\0';
  s_messageReady = 0U;
  USART_ITConfig(ESP8266_USART, USART_IT_RXNE, ENABLE);
  return 1U;
}

uint8_t ESP8266_Task(void) {
  static uint8_t retryCycles = 0U;
  char buffer[ESP8266_SEND_BUFFER_SIZE];
  uint16_t length = 0U;

  if (s_wifiConnected == 0U) {
    if (retryCycles < ESP8266_RETRY_CYCLES) {
      retryCycles++;
      return ESP8266_STATUS_WIFI_ERROR;
    }

    retryCycles = 0U;
    return ESP8266_STATUS_RECONNECT_REQUIRED;
  }
  retryCycles = 0U;

  if (s_tcpConnected == 0U) {
    buffer[0] = '\0';
    (void)ESP8266_AppendString(buffer, sizeof(buffer), &length,
                               "AT+CIPSTART=\"TCP\",\"");
    (void)ESP8266_AppendString(buffer, sizeof(buffer), &length, SERVER_IP);
    (void)ESP8266_AppendString(buffer, sizeof(buffer), &length, "\",");
    (void)ESP8266_AppendString(buffer, sizeof(buffer), &length, SERVER_PORT);
    ESP8266_SendCommand(buffer);

    if (ESP8266_WaitFor("CONNECT", "ALREADY CONNECTED", 5000U) == 0U) {
      return ESP8266_STATUS_TCP_ERROR;
    }

    s_tcpConnected = 1U;
  }

  if (s_registered == 0U) {
    buffer[0] = '\0';
    length = 0U;
    (void)ESP8266_AppendString(buffer, sizeof(buffer), &length, "REG|");
    (void)ESP8266_AppendString(buffer, sizeof(buffer), &length, DEVICE_ID);
    (void)ESP8266_AppendChar(buffer, sizeof(buffer), &length, '\n');

    if (ESP8266_SendPayload(buffer) == 0U) {
      s_tcpConnected = 0U;
      return ESP8266_STATUS_SEND_ERROR;
    }
    s_registered = 1U;
    return ESP8266_STATUS_OK;
  }

  buffer[0] = '\0';
  length = 0U;
  (void)ESP8266_AppendString(buffer, sizeof(buffer), &length, "APP001|");
  (void)ESP8266_AppendUnsigned(buffer, sizeof(buffer), &length, s_temperature);
  (void)ESP8266_AppendChar(buffer, sizeof(buffer), &length, '|');
  (void)ESP8266_AppendUnsigned(buffer, sizeof(buffer), &length, s_humidity);
  (void)ESP8266_AppendChar(buffer, sizeof(buffer), &length, '|');
  (void)ESP8266_AppendUnsigned(buffer, sizeof(buffer), &length, s_gasPpm);
  (void)ESP8266_AppendChar(buffer, sizeof(buffer), &length, '\n');

  if (ESP8266_SendPayload(buffer) == 0U) {
    s_tcpConnected = 0U;
    s_registered = 0U;
    return ESP8266_STATUS_SEND_ERROR;
  }

  return ESP8266_STATUS_OK;
}
