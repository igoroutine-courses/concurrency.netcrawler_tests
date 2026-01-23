# Network crawler

(основано на задаче из собеседования в синюю BigTech компанию)

## Описание

Вам необходимо реализовать сервер, который занимается обходом URL'ов

```Go
type Crawler interface {
	ListenAndServe(ctx context.Context, address string) error
}

type CrawlRequest struct {
	URLs      []string `json:"urls"`
	Workers   int      `json:"workers"`    // количество воркеров
	TimeoutMS int      `json:"timeout_ms"` // таймаут на обработку всех урлов
}

type CrawlResponse struct {
	URL        string `json:"url"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
}
```

Отправляя `POST` запрос на `/crawl` сервер должен многопоточно обойти `URLs` и вернуть коды ответов:

```go
package main

import (
	"context"

	"github.com/igoroutine-courses/gonature.concrypto/internal/crawler"
)

func main() {
	ctx := context.Background()

	c := crawler.New()
	c.ListenAndServe(ctx, ":8080")

}
```

**Request:**
```
curl --location 'http://127.0.0.1:8080/crawl' \
--header 'Content-Type: application/json' \
--data '{
    "urls": [
        "https://google.com",
        "https://dzen.ru"
    ],
    "workers": 2,
    "timeout_ms": 2000
}
'
```

**Response:**

```
[
    {
        "url": "https://google.com",
        "status_code": 200
    },
    {
        "url": "https://dzen.ru",
        "status_code": 200
    }
]
```

* В ответе для урла должна быть либо ошибка `error`, либо `status_code`


* Порядок результатов должен совпадать с входным

## Задание

При реализации этой задачи рекомендуется использовать паттерны `generator` и `workerpool`

```go
func Generate[T, R any](ctx context.Context, data []T, f func(index int, e T) R, size int) <-chan R {
	result := make(chan R, size)

	go func() {
		defer close(result)

		for i := 0; i < len(data); i++ {
			select {
			case result <- f(i, data[i]):
			case <-ctx.Done():
				return
			}
		}
	}()

	return result
}
```

```go
func Run[T any](
	ctx context.Context,
	workersCount int,
	input <-chan T,
	f func(e T),
) {
	wg := new(sync.WaitGroup)

	for i := 0; i < workersCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for {
				select {
				case <-ctx.Done():
					return
				case v, ok := <-input:
					if !ok {
						return
					}

					select {
					case <-ctx.Done():
						return
					default:
						f(v)
					}
				}
			}
		}()
	}

	wg.Wait()
}
```

Более того, реализация должна поддерживать:
* Кэширование ответов c (ttl = 1 секунда).
* Нормализацию URL'ов.
* [Graceful shutdown](https://cs.opensource.google/go/go/+/refs/tags/go1.25.5:src/net/http/server.go;l=3179) - при отмене контекста сервер должен вызвать Shutdown и корректно дождаться завершения текущих запросов (до 10 секунд).
* Кэширование для параллельных запросов ([singleflight](https://pkg.go.dev/golang.org/x/sync/singleflight)).

Для `singleflight` рекомендуется использовать generic-обёртку:

```go
func (s *singleFlightImpl[T]) Do(key string, fn func() (T, error)) (T, bool, error) {
	v, err, shared := s.sf.Do(key, func() (any, error) {
		return fn()
	})

	return v.(T), err, shared
}
```

Также рекомендуется использовать паттерн `withLock`:

```go
func withLock(mutex sync.Locker, action func()) {
	mutex.Lock()
	defer mutex.Unlock()

	action()
}
```

## Сдача
* Решение необходимо реализовать в файле [crawler.go](./internal/crawler/crawler.go)
* Открыть pull request из ветки `hw` в ветку `main` **вашего репозитория**
* В описании PR заполнить количество часов, которые вы потратили на это задание
* Не стоит изменять файлы в директории [.github](.github)

## Особенности реализации
* `sync.Pool` использовать не требуется
* Используйте тесты, чтобы заполнить недосказанности, в них в том числе есть подсказки

## Скрипты
Для запуска скриптов на курсе необходимо установить [go-task](https://taskfile.dev/docs/installation)

`go install github.com/go-task/task/v3/cmd/task@latest`

Перед выполнением задания не забудьте выполнить:

```bash 
task update
```

Запустить линтер:
```bash 
task lint
```

Запустить тесты:
```bash
task test
``` 

Обновить файлы задания
```bash
task update
```

Принудительно обновить файлы задания
```bash
task force-update
```

Скрипты работают на Windows, однако при разработке на этой операционной системе
рекомендуется использовать [WSL](https://learn.microsoft.com/en-us/windows/wsl/install)
