# brsaver

Telegram-бот на Go, який знаходить у повідомленнях групи посилання на TikTok, YouTube Shorts, Instagram та Facebook, завантажує відео через `yt-dlp`, надсилає його відповіддю як Telegram-відео й одразу видаляє тимчасові файли.

## Можливості

- працює в групах і супергрупах через Telegram long polling;
- розпізнає звичайні URL і приховані `text_link`-посилання;
- підтримує TikTok, YouTube Shorts, Instagram Reel/Post/TV та Facebook Reel/Watch/Video;
- завантажує кілька посилань паралельно через обмежену чергу воркерів;
- обмежує тривалість, кількість посилань і розмір файлу (за замовчуванням 49 МіБ);
- автоматично стискає завеликі відео у H.264/AAC за допомогою двопрохідного `ffmpeg`;
- зберігає тему forum-групи та відповідає на оригінальне повідомлення;
- створює окремий каталог для кожного завантаження і видаляє його після успіху або помилки;
- не передає URL у shell: `yt-dlp` запускається напряму з масивом аргументів.
- підтримує browser impersonation через `curl_cffi` для TikTok-сторінок із TLS/JS challenge.

## Швидкий запуск через Docker Compose

1. Створіть бота через [@BotFather](https://t.me/BotFather) і скопіюйте token.
2. У BotFather виконайте `/setprivacy`, виберіть бота й натисніть **Disable**. Інакше Telegram не надсилатиме боту звичайні повідомлення групи.
3. Додайте бота до потрібної групи.
4. Створіть локальний `.env`:

   ```bash
   cp .env.example .env
   ```

   Запишіть у нього справжній `TELEGRAM_BOT_TOKEN`. Щоб обмежити бота конкретними групами, вкажіть їхні ID у `ALLOWED_CHAT_IDS`.

5. Запустіть:

   ```bash
   docker compose up --build -d
   docker compose logs -f brsaver
   ```

Контейнер має read-only root filesystem. Завантаження відбуваються лише у тимчасовій файловій системі `/tmp`, яка зникає разом із контейнером; застосунок додатково видаляє каталог кожної задачі одразу після надсилання.

Події та помилки пишуться у Docker logs. Для журналу використовується драйвер `local` із ротацією: до п'яти файлів по 10 МБ. Перегляд:

```bash
docker compose logs -f brsaver
```

Логи містять тип події, provider, chat ID та message ID, але не містять bot token або повний URL медіа.

## Локальний запуск

Потрібні Go 1.24+, актуальний nightly [`yt-dlp`](https://github.com/yt-dlp/yt-dlp) та `ffmpeg` у `PATH`. Nightly використовується навмисно: TikTok, Instagram та інші сайти часто змінюють extractor API раніше за наступний stable-реліз.

```bash
cp .env.example .env
set -a
. ./.env
set +a
go run ./cmd/brsaver
```

## Конфігурація

| Змінна | Типове значення | Призначення |
|---|---:|---|
| `TELEGRAM_BOT_TOKEN` | — | Обов'язковий token від BotFather |
| `ALLOWED_CHAT_IDS` | усі групи | Список дозволених chat ID через кому |
| `WORKERS` | `2` | Кількість паралельних завантажень |
| `QUEUE_SIZE` | `32` | Максимум задач у черзі |
| `MAX_LINKS_PER_MESSAGE` | `3` | Максимум оброблених URL з одного повідомлення |
| `MAX_FILE_SIZE_MB` | `49` | Ліміт відео; стандартний Bot API приймає до 50 МБ |
| `MAX_DOWNLOAD_SIZE_MB` | `250` | Максимальний розмір оригіналу до стиснення |
| `DOWNLOAD_TIMEOUT` | `4m` | Тайм-аут одного завантаження |
| `DOWNLOAD_ATTEMPTS` | `3` | Кількість спроб при тимчасовій помилці джерела |
| `DOWNLOAD_RETRY_DELAY` | `2s` | Пауза між повторними спробами |
| `COMPRESSION_TIMEOUT` | `12m` | Тайм-аут двопрохідного перекодування |
| `UPLOAD_TIMEOUT` | `3m` | Тайм-аут надсилання до Telegram |
| `COMPRESS_OVERSIZED` | `true` | Автоматично стискати відео понад upload-ліміт |
| `NOTIFY_ERRORS` | `true` | Повідомляти в групу про невдале завантаження |
| `DOWNLOAD_DIR` | `/tmp/brsaver` | Кореневий каталог тимчасових задач |
| `YT_DLP_PATH` | `yt-dlp` | Шлях до виконуваного файла |
| `YT_DLP_FORMAT` | MP4/H.264-preferred | Власний format selector для `yt-dlp` |
| `FFMPEG_PATH` / `FFPROBE_PATH` | `ffmpeg` / `ffprobe` | Шляхи до інструментів перекодування |
| `YT_DLP_COOKIES_FILE` | порожньо | Netscape cookies-файл для доступного вам контенту |
| `TELEGRAM_API_BASE_URL` | `https://api.telegram.org` | URL Bot API, зокрема локального сервера |

Для cookies не додавайте файл до Git. У Docker його можна змонтувати read-only і задати шлях змінною середовища:

```yaml
services:
  brsaver:
    volumes:
      - ./cookies.txt:/run/secrets/cookies.txt:ro
    environment:
      YT_DLP_COOKIES_FILE: /run/secrets/cookies.txt
```

## Перевірки

```bash
go test ./...
go vet ./...
```

Завантажуйте й повторно публікуйте лише контент, на який маєте відповідні права. Приватні або захищені платформою матеріали можуть бути недоступні навіть із cookies.
