# Notion2API — агентный мост Notion AI → OpenAI API

Мост с открытым кодом на Go: превращает подписку **Notion AI** в API, совместимый с OpenAI.
Главная фишка этого форка — поддержка **tool_calls (function calling)** на уровне гейтвея:
модели из Notion можно использовать как агентов — с чтением файлов, вызовом инструментов,
мультитуром — в OpenCode, Cursor, Droid, Windsurf и любом клиенте, который умеет function calling.

> Неофициальный reverse-engineered инструмент, использует внутренний веб-API Notion.
> Работает через твою подписку Notion AI (включая Business Trial).

---

## Что умеет

- **OpenAI-совместимые эндпоинты:**
  - `GET /v1/models` — список доступных моделей
  - `POST /v1/chat/completions` — чат (стриминг SSE + обычный режим)
  - `GET /healthz` — проверка состояния
- **Tool calls (function calling):** гейтвей подмешивает схемы инструментов в системный
  промпт, модель отвечает строгим XML-блоком, гейтвей превращает его в нормальный
  `tool_calls` в ответе. Результаты инструментов (`role: tool`) принимаются обратно —
  полный агентский цикл работает.
- **17 моделей**, включая **GPT-6 Astra** (коднейм Notion `orlando-quinn`).
- **Мультиаккаунт** с балансировкой и кулдауном упавших аккаунтов.
- **Админка** в браузере: аккаунты, импорт сессии, конфиг, диалоги, метрики.
- **Веб-поиск Notion AI**, свежий тред на каждый запрос (меньше глюков контекста).
- Сохранение сессий в SQLite.

---

## Поддерживаемые модели

| Model ID | Что это | Провайдер |
|---|---|---|
| `gpt-6-astra` | GPT-6 Astra (флагман) | OpenAI |
| `opus-4.8` | Claude Opus 4.8 | Anthropic |
| `opus-4.7` | Claude Opus 4.7 | Anthropic |
| `opus-4.6` | Claude Opus 4.6 | Anthropic |
| `sonnet-4.6` | Claude Sonnet 4.6 | Anthropic |
| `haiku-4.5` | Claude Haiku 4.5 (быстрая) | Anthropic |
| `gpt-5.5` | GPT-5.5 | OpenAI |
| `gpt-5.4` | GPT-5.4 | OpenAI |
| `gpt-5.4-mini` | GPT-5.4 Mini | OpenAI |
| `gpt-5.4-nano` | GPT-5.4 Nano | OpenAI |
| `gpt-5.2` | GPT-5.2 | OpenAI |
| `gemini-3.1-pro` | Gemini 3.1 Pro | Google |
| `gemini-3-flash` | Gemini 3 Flash | Google |
| `gemini-2.5-flash` | Gemini 2.5 Flash | Google |
| `grok-4.3` | Grok 4.3 | xAI |
| `minimax-m2.5` | MiniMax M2.5 | MiniMax |
| `auto` | автовыбор (deprecated) | system |

Актуальный список всегда можно получить запросом `GET /v1/models`.

---

## Быстрый старт

### 1. Требования

- Go 1.25+ (только для сборки из исходников)
- Аккаунт Notion с доступом к Notion AI (подойдёт Business Trial)
- Linux / macOS (на Windows — через WSL)

### 2. Сборка

```bash
git clone https://github.com/npukolno/notion2api.git
cd notion2api
go build -o notion2api-agent ./cmd/notion2api/
```

### 3. Конфиг

Создай `config.json` (можно скопировать из `config.example.json` и урезать):

```json
{
  "host": "127.0.0.1",
  "port": 8787,
  "api_key": "придумай-секретный-ключ",
  "default_model": "gpt-6-astra",
  "timeout_sec": 180,
  "admin": {
    "enabled": true,
    "password": "придумай-пароль-админки"
  },
  "features": {
    "use_web_search": true,
    "force_fresh_thread_per_request": true
  },
  "accounts": []
}
```

- `api_key` — ключ, который будешь указывать в клиентах (`Authorization: Bearer ...`).
- Аккаунты Notion удобнее добавлять через админку (шаг 5), поэтому `accounts` пока пустой.

### 4. Запуск

```bash
./notion2api-agent --config ./config.json
```

Сервер поднимется на `http://127.0.0.1:8787`.

### 5. Подключение аккаунта Notion

Нужно всего три вещи: **email**, **данные воркспейса** и **свежая кука `token_v2`**.

**5.1. Узнай ID воркспейса (один раз):**

1. Открой `https://www.notion.so/ai` и залогинься.
2. Нажми F12 → вкладка **Console**, вставь скрипт `scripts/extract_notion_info.js` (лежит в репозитории), нажми Enter.
3. Скрипт покажет JSON с `space_id`, `user_id`, `space_view_id`, именем и почтой. Сохрани его.

**5.2. Скопируй свежую куку:**

F12 → **Application** → **Cookies** → `https://www.notion.so` → скопируй значение `token_v2`.
Кука периодически протухает — это нормально, ниже написано как обновлять.

**5.3. Импортируй аккаунт через админку:**

```bash
# 1. логин в админку (сохранит куку notion2api_admin)
curl -c admin.cookie -s http://127.0.0.1:8787/admin/login \
  -H 'Content-Type: application/json' \
  -d '{"password":"твой-пароль-админки"}'

# 2. импорт аккаунта (client_version подтянется сам)
curl -b admin.cookie -s http://127.0.0.1:8787/admin/accounts/manual \
  -H 'Content-Type: application/json' \
  -d '{
    "email": "твоя-почта@gmail.com",
    "user_id": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
    "user_name": "твоё-имя",
    "space_id": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
    "space_view_id": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
    "space_name": "твой-воркспейс",
    "cookie_header": "token_v2=ВСТАВЬ_СВЕЖИЙ_TOKEN_V2",
    "active": true
  }'
```

В ответе должно быть `"success":true, "status":"ready"`. Готово — аккаунт заведён,
админка также доступна в браузере по `http://127.0.0.1:8787/admin/`.

### Второй воркспейс (расширение лимитов)

Ошибка `dispatch capacity exceeded: too many concurrent requests` означает, что все
слоты аккаунтов заняты (по умолчанию слот всего один на аккаунт). Два рычага:

1. **Поднять `max_concurrency`** аккаунту (по 2–3 на воркспейс, больше — риск 429
   от самого Notion):
   ```bash
   curl -b admin.cookie -s http://127.0.0.1:8787/admin/accounts \
     -H 'Content-Type: application/json' \
     -d '{"email":"твоя-почта@gmail.com","max_concurrency":2}'
   ```
2. **Добавить второй воркспейс** того же аккаунта отдельной записью — у него свой
   спейс, свои лимиты и свои слоты. Важно: ключ записи (`email`) должен отличаться,
   иначе импорт перезапишет первый воркспейс. Рабочий приём — плюс-алиас почты
   (для Gmail это тот же ящик), а внутри — реальные ID второго воркспейса:
   ```bash
   curl -b admin.cookie -s http://127.0.0.1:8787/admin/accounts/manual \
     -H 'Content-Type: application/json' \
     -d '{
       "email": "твоя-почта+ws2@gmail.com",
       "user_id": "тот-же-user-id",
       "user_name": "твоё-имя",
       "space_id": "space_id-ВТОРОГО-воркспейса",
       "space_view_id": "space_view_id-ВТОРОГО-воркспейса",
       "cookie_header": "token_v2=ТА-ЖЕ-свежая-кука",
       "active": false
     }'
   ```
   ID второго воркспейса берутся тем же скриптом `scripts/extract_notion_info.js`
   (выбери другой воркспейс в промпте). Проверка — `GET /admin/accounts` должен
   показать обе записи со статусом `ready` и разными `space_id`.

> Важно: вторая запись делит одну сессию Notion с первой. Поэтому у неё надо
> отключить email-перелогин, иначе мост будет слать signup-код на адрес-алиас
> (такого логина в Notion нет — только спам и статус `failed`):
> ```bash
> curl -b admin.cookie -s http://127.0.0.1:8787/admin/accounts \
>   -H 'Content-Type: application/json' \
>   -d '{"email":"твоя-почта+ws2@gmail.com","disable_auto_relogin":true}'
> ```
> Когда сессия умрёт, перелогинься один раз через основную запись
> (`/admin/accounts/login/start` + `/login/verify` с кодом из письма),
> а свежие куки перелей во вторую запись скриптом `sync_cookies.py`-типа:
> скопируй массив `cookies` из её `probe.json` в `probe.json` второй записи
> (поля `space_id`/`space_view_id` второй записи не трогать) и рестартни сервис.

> Кука — это полный доступ к твоему Notion. Не коммить её, не кидай в чаты,
> файл с кукой храни с правами `chmod 600`.

### 6. Проверка

```bash
# список моделей
curl -s http://127.0.0.1:8787/v1/models \
  -H "Authorization: Bearer твой-api-ключ" | python3 -m json.tool | head -n 20

# простой чат
curl -s http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer твой-api-ключ" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-6-astra",
       "messages":[{"role":"user","content":"Ответь ровно: ASTRA_OK"}],
       "stream": false}'
```

---

## Tool calling (как это работает)

Обычный `notion2api` инструменты не умеет — шлёт только текст. Здесь гейтвей делает три вещи:

1. **Туда:** схемы твоих `tools` превращаются в строгую XML-инструкцию
   (`<tools_available>` + формат `<tool_calls><call>...`) и добавляются в системный промпт.
2. **Сюда:** если модель ответила XML-блоком, гейтвей парсит его и отдаёт нормальный
   `tool_calls` с `finish_reason: tool_calls` — клиент исполняет вызовы сам.
3. **Обратно:** сообщения с `role: tool` превращаются обратно в текст
   (`[Tool Result] ...`) и уходят модели следующим туром.

Плюс есть детерминированный синтез: если в промпте явно названа тула и виден аргумент
(путь с `/`, команда, паттерн), гейтвей соберёт `tool_calls` сам, даже если модель
ответила прозой.

Пример — просим прочитать файл:

```bash
curl -s http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer твой-api-ключ" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-6-astra",
       "messages":[{"role":"user",
         "content":"read the file /home/user/project/config.json and tell me the port"}],
       "tools":[{"type":"function","function":{
         "name":"read_file","description":"Read a file from disk",
         "parameters":{"type":"object",
           "properties":{"path":{"type":"string"}},"required":["path"]}}}],
       "tool_choice":"auto","stream":false}'
# → {"choices":[{"finish_reason":"tool_calls","message":{
#      "role":"assistant","content":null,
#      "tool_calls":[{"id":"call_0","type":"function",
#        "function":{"name":"read_file",
#          "arguments":"{\"path\":\"/home/user/project/config.json\"}"}}]}}]}
```

Дальше клиент исполняет `read_file` локально и шлёт результат назад:

```json
{"role":"tool","tool_call_id":"call_0","content":"{\"host\":\"127.0.0.1\",\"port\":8787}"}
```

— модель отвечает уже по факту: `The port number is 8787.`

Python-пример:

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8787/v1", api_key="твой-api-ключ")

tools = [{"type": "function", "function": {
    "name": "read_file", "description": "Read a file from disk",
    "parameters": {"type": "object",
        "properties": {"path": {"type": "string"}},
        "required": ["path"]}}}]

first = client.chat.completions.create(
    model="gpt-6-astra",
    messages=[{"role": "user",
               "content": "read the file /home/user/project/config.json"}],
    tools=tools, tool_choice="auto")

msg = first.choices[0].message
if msg.tool_calls:
    call = msg.tool_calls[0]
    result = open("/home/user/project/config.json").read()  # исполняешь сам
    second = client.chat.completions.create(
        model="gpt-6-astra",
        messages=[
            {"role": "user",
             "content": "read the file /home/user/project/config.json"},
            {"role": "assistant", "content": msg.content,
             "tool_calls": [{"id": call.id, "type": "function",
                 "function": {"name": call.function.name,
                              "arguments": call.function.arguments}}]},
            {"role": "tool", "tool_call_id": call.id, "content": result}],
        tools=tools)
    print(second.choices[0].message.content)
else:
    print(msg.content)
```

---

## Подключение к OpenCode (агент с файлами и инструментами)

В `opencode.jsonc` добавь провайдера:

```jsonc
"notion": {
  "api": "openai",
  "name": "Notion AI",
  "options": {
    "baseURL": "http://127.0.0.1:8787/v1",
    "apiKey": "твой-api-ключ"
  },
  "models": {
    "gpt-6-astra": {"name": "Notion | GPT-6 Astra",
      "tool_call": true, "reasoning": true, "attachment": true,
      "temperature": true,
      "modalities": {"input": ["text", "image"], "output": ["text"]}}
  }
}
```

Перезапусти OpenCode (конфиг читается на старте) и выбери модель `notion/gpt-6-astra`.
Агент сможет вызывать инструменты, читать/писать файлы через тулколлы моста.

> Честная оговорка: синтез тулколлов — эмуляция поверх чата, а не нативный
> function calling (его нет в самом Notion AI). Лучше всего срабатывает, когда
> в задаче есть конкретные пути/команды. Иногда модель отвечает прозой вместо
> вызова — тогда агент просто покажет текст.

---

## Автозапуск (systemd, Linux)

```bash
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/notion-agent.service <<'EOF'
[Unit]
Description=Notion2API agent bridge
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/home/USER/notion2api
ExecStart=/home/USER/notion2api/notion2api-agent --config /home/USER/notion2api/config.json
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable --now notion-agent
systemctl --user status notion-agent
journalctl --user -u notion-agent -f   # логи
```

---

## Частые проблемы

| Симптом | Причина и что делать |
|---|---|
| `401 Token was invalid or expired` | Кука `token_v2` протухла. Скопируй свежую из браузера и повтори импорт через `/admin/accounts/manual`. |
| `429` / «Notion AI приостановлен» | Notion режет частые запросы, триальные воркспейсы — особенно. Добавь второй аккаунт (автоматом будет round-robin), снизь темп агентских циклов. |
| `403 trust-rule-denied` | Notion не доверяет сети/VPS. Запускай мост на той же домашней машине/сети, где сидишь в браузере, либо ходи в интернет через домашний IP. |
| Первый токен идёт ~3 сек | Это задержка самого Notion AI, не лечится. Для «переводчиков» и прочего realtime не годится. |
| Модель отвечает текстом вместо `tool_calls` | Назови тулу и путь явно в промпте; проверь, что клиент реально шлёт `tools` в запросе. |
| Админка пишет `admin authentication required` | Сначала `POST /admin/login` с паролем, дальше ходи с кукой `notion2api_admin` или заголовком `X-Admin-Token`. |

---

## Как добавить новую модель

Коднейм Notion подсматривается в сетевых запросах веба Notion AI, дальше правится один файл:

1. `internal/app/models.go` → `builtinModelDefinitions()` — добавить строку:
   `{ID: "gpt-6-astra", Name: "GPT-6 Astra", NotionModel: "orlando-quinn",
     Family: "openai", Group: "intelligent", Beta: true, Enabled: true,
     Aliases: []string{"gpt6astra", "orlando-quinn"}}`
2. Пересобрать, проверить `GET /v1/models`.

---

## Благодарности

- Оригинал: [GALIAIS/Notion2API](https://github.com/GALIAIS/Notion2API)
- Tool-calls форк: [sadada754/API_Notion](https://github.com/sadada754/API_Notion)
- Апстрим Python-версии: [maverickxone/notion2api](https://github.com/maverickxone/notion2api)
  (оттуда взят коднейм `gpt-6-astra → orlando-quinn`)

## Лицензия

MIT — см. файл `LICENSE`.
