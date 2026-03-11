# SimpleMon v1.0.0

Лёгкий демон мониторинга Linux-сервера на Go + однофайловый HTML-фронтенд.  
Без баз данных, брокеров и внешних зависимостей.

```
CPU · RAM · Swap · Network interfaces · Custom widgets
```

---

## Возможности

- **Метрики**: CPU (total + per core), RAM, Swap, сетевые интерфейсы
- **Сеть**: скорость rx/tx (bit/s), PPS (пакеты/сек), ошибки/сек
- **Custom widgets**: числовой график (`graph`) или текстовый вывод (`text`) из любой shell-команды
- **История**: настраиваемая глубина (по умолчанию 30 минут), выбор диапазона прямо на странице
- **Фронтенд**: один HTML-файл, без Node.js и сборщиков
- **API**: JSON с CORS, легко интегрируется с любым дашбордом
- **Деплой**: systemd unit, `make install` / `make uninstall`

---

## Структура проекта

```
simplemon/
├── main.go                        # точка входа
├── internal/
│   ├── api/                       # HTTP JSON API
│   ├── collector/                 # сбор системных метрик (gopsutil)
│   ├── config/                    # YAML-конфиг
│   ├── ringbuf/                   # кольцевой буфер
│   └── widget/                    # кастомные виджеты
├── etc/simplemon.yaml             # конфиг по умолчанию
├── systemd/simplemon.service      # systemd unit
├── html
|   ├── simplemon.html             # фронтенд
├── Makefile
└── go.mod
```

---

## Сборка

**Требования**: Linux, Go ≥ 1.21

```bash
git clone https://github.com/your/simplemon
cd simplemon
go mod tidy
make build
# результат: bin/simplemon
```

---

## Установка

### Через make (рекомендуется)

```bash
sudo make install
sudo systemctl enable --now simplemon
systemctl status simplemon
```

Удаление:

```bash
sudo make uninstall
```

---

### Ручная установка

**1. Собрать бинарник**

```bash
go mod tidy
make build
```

**2. Создать системного пользователя**

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin \
     --comment "SimpleMon monitoring daemon" simplemon
```

**3. Установить бинарник**

```bash
sudo install -m 0755 bin/simplemon /usr/local/bin/simplemon
```

**4. Конфигурация**

```bash
sudo mkdir -p /etc/simplemon
sudo install -m 0640 -o root -g simplemon \
     etc/simplemon.yaml /etc/simplemon/simplemon.yaml
# при необходимости отредактировать:
sudo nano /etc/simplemon/simplemon.yaml
```

**5. Systemd unit**

```bash
sudo install -m 0644 systemd/simplemon.service \
     /etc/systemd/system/simplemon.service
sudo systemctl daemon-reload
```

**6. Запуск**

```bash
sudo systemctl enable simplemon
sudo systemctl start simplemon
```

**7. Проверка**

```bash
systemctl status simplemon
journalctl -u simplemon -f
curl -s http://127.0.0.1:8095/health
```

---

## Фронтенд

Открыть `simplemon.html` в браузере.

### Nginx proxy (рекомендуется)

Позволяет открывать фронтенд на порту 80, бэкенд остаётся на `127.0.0.1`.  
В `HOSTS` оставьте `url: ''` — браузер обращается к тому же хосту, nginx проксирует запросы.

```nginx
server {
    listen 80 default_server;

    location / {
        root /var/www/simplemon;
        index simplemon.html;
    }
    location /metrics/ { proxy_pass http://127.0.0.1:8095; }
    location /info      { proxy_pass http://127.0.0.1:8095/info; }
    location /widgets   { proxy_pass http://127.0.0.1:8095/widgets; }
    location /health    { proxy_pass http://127.0.0.1:8095/health; }
}
```

```bash
sudo mkdir -p /var/www/simplemon
sudo cp simplemon.html /var/www/simplemon/
```

### Прямое подключение / несколько серверов

Отредактировать массив `HOSTS` в конце файла `simplemon.html`:

```javascript
const HOSTS = [
  { label: 'localhost',     url: '' },                      // nginx proxy (по умолчанию)
  // { label: 'direct',    url: 'http://127.0.0.1:8095' }, // прямое подключение
  // { label: 'server-1',  url: 'http://192.168.1.10:8095' },
  // { label: 'server-2',  url: 'http://192.168.1.11:8095' },
];
```

При нескольких хостах в хедере появляется выпадающий список с кнопкой Connect.

---

## Конфигурация

Файл: `/etc/simplemon/simplemon.yaml`

```yaml
listen:
  address: "127.0.0.1"   # 0.0.0.0 — слушать на всех интерфейсах
  port: 8095

interfaces:
  include:
    - ".*"               # все интерфейсы (regexp)
    # - "eth.*"
    # - "ens.*"

collector:
  interval_seconds: 3      # интервал сбора метрик
  retention_seconds: 1800  # глубина хранения (30 мин = 600 точек)

# Кастомные виджеты (опционально)
widgets:
  - name: "Open file descriptors"
    type: graph              # числовой график
    command: "cat /proc/sys/fs/file-nr | awk '{print $1}'"
    interval_seconds: 5
    unit: "fd"

  - name: "Disk usage"
    type: text               # текстовый вывод команды
    command: "df -h"
```

После изменения конфига:

```bash
sudo systemctl restart simplemon
```

---

## API

Все эндпоинты: GET, CORS `*`.

| Эндпоинт | Описание | Рекомендуемый интервал |
|---|---|---|
| `GET /health` | `{"status":"ok"}` | liveness probe |
| `GET /info` | аптайм, интерфейсы, ядра, параметры | раз в минуту |
| `GET /metrics/full` | полная история (до `retention_seconds`) | один раз при старте |
| `GET /metrics/last` | последние 20 точек (~60 сек) | каждые `interval_seconds` |
| `GET /widgets` | данные кастомных виджетов | каждые `interval_seconds` |

**Единицы**: скорости сети (`rx_rate_bits`, `tx_rate_bits`) — биты/сек.  
Перевод: `÷ 1 000` → kbps, `÷ 1 000 000` → Mbps.

### Примеры curl

```bash
curl -s http://127.0.0.1:8095/health
curl -s http://127.0.0.1:8095/info         | python3 -m json.tool
curl -s http://127.0.0.1:8095/metrics/last | python3 -m json.tool
curl -s http://127.0.0.1:8095/metrics/full | python3 -m json.tool
curl -s http://127.0.0.1:8095/widgets      | python3 -m json.tool
```

---

## Зависимости

| Пакет | Назначение |
|---|---|
| [gopsutil/v3](https://github.com/shirou/gopsutil) | системные метрики |
| [yaml.v3](https://gopkg.in/yaml.v3) | парсинг конфига |
| [Chart.js 4.4.1](https://www.chartjs.org/) | графики (CDN, только фронтенд) |

---

## Удаление

```bash
sudo make uninstall
```

Или вручную:

```bash
sudo systemctl stop simplemon && sudo systemctl disable simplemon
sudo rm /etc/systemd/system/simplemon.service && sudo systemctl daemon-reload
sudo rm /usr/local/bin/simplemon
sudo rm -rf /etc/simplemon
sudo userdel simplemon
```
