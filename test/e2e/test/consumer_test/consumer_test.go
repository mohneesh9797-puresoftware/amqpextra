package consumer_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/makasim/amqpextra/test/e2e/helper/rabbitmq"

	"github.com/makasim/amqpextra"

	"github.com/streadway/amqp"

	"github.com/makasim/amqpextra/test/e2e/helper/logger"

	"github.com/stretchr/testify/assert"
)

func TestCloseChannelOnAlreadyClosedConnection(t *testing.T) {
	l := logger.New()

	conn := amqpextra.Dial([]string{"amqp://guest:guest@rabbitmq:5672/amqpextra"})

	worker := amqpextra.WorkerFunc(func(msg amqp.Delivery, ctx context.Context) interface{} {
		return nil
	})

	go func() {
		<-time.NewTimer(time.Second).C

		conn.Close()
	}()

	c := conn.Consumer(rabbitmq.Queue2(conn), worker)
	c.SetLogger(l)

	c.Run()

	expected := `[DEBUG] consumer starting
[DEBUG] workers started
[DEBUG] workers stopped
[DEBUG] consumer stopped
`
	assert.Equal(t, expected, l.Logs())

	assert.NotContains(t, l.Logs(), "Exception (504) Reason: \"channel/connection is not open\"\n[DEBUG] consumer stopped\n")
}

func TestConsumeOneAndCloseConsumer(t *testing.T) {
	l := logger.New()

	conn := amqpextra.Dial([]string{"amqp://guest:guest@rabbitmq:5672/amqpextra"})
	defer conn.Close()

	conn.SetLogger(l)

	queue := rabbitmq.Queue2(conn)
	rabbitmq.Publish2(conn, "testbdy", queue)

	var c *amqpextra.Consumer
	c = conn.Consumer(queue, amqpextra.WorkerFunc(func(msg amqp.Delivery, ctx context.Context) interface{} {
		l.Printf("[DEBUG] got message %s", msg.Body)

		msg.Ack(false)

		c.Close()

		return nil
	}))

	c.Run()

	expected := `[DEBUG] connection established
[DEBUG] consumer starting
[DEBUG] workers started
[DEBUG] got message testbdy
[DEBUG] workers stopped
[DEBUG] consumer stopped
`
	assert.Equal(t, expected, l.Logs())
}

func TestCongruentlyPublishConsumeWhileConnectionLost(t *testing.T) {
	l := logger.New()

	connName := fmt.Sprintf("amqpextra-test-%d", time.Now().UnixNano())

	conn := amqpextra.DialConfig([]string{"amqp://guest:guest@rabbitmq:5672/amqpextra"}, amqp.Config{
		Properties: amqp.Table{
			"connection_name": connName,
		},
	})
	defer conn.Close()

	conn.SetLogger(l)

	var wg sync.WaitGroup

	wg.Add(1)
	go func(connName string, wg *sync.WaitGroup) {
		defer wg.Done()

		<-time.NewTimer(time.Second * 5).C

		if !assert.True(t, rabbitmq.CloseConn(connName)) {
			return
		}
	}(connName, &wg)

	queue := rabbitmq.Queue2(conn)

	var countPublished uint32
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(extraconn *amqpextra.Connection, queue string, wg *sync.WaitGroup) {
			defer wg.Done()

			connCh, closeCh := extraconn.Get()

			ticker := time.NewTicker(time.Millisecond * 100)
			timer := time.NewTimer(time.Second * 10)

		L1:
			for conn := range connCh {
				ch, err := conn.Channel()
				assert.NoError(t, err)

				_, err = ch.QueueDeclare(queue, true, false, false, false, nil)
				assert.NoError(t, err)

				for {
					select {
					case <-ticker.C:
						err := ch.Publish("", queue, false, false, amqp.Publishing{})

						if err == nil {
							atomic.AddUint32(&countPublished, 1)
						}
					case <-closeCh:
						continue L1
					case <-timer.C:
						break L1
					}
				}

			}

		}(conn, queue, &wg)
	}

	var countConsumed uint32

	c := conn.Consumer(queue, amqpextra.WorkerFunc(func(msg amqp.Delivery, ctx context.Context) interface{} {
		atomic.AddUint32(&countConsumed, 1)

		msg.Ack(false)

		return nil
	}))
	c.SetWorkerNum(5)

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-time.NewTimer(time.Second * 11).C

		c.Close()
	}()

	c.Run()
	wg.Wait()

	expected := `[DEBUG] connection established
[DEBUG] consumer starting
[DEBUG] workers started
[DEBUG] workers stopped
[DEBUG] connection established
[DEBUG] consumer starting
[DEBUG] workers started
[DEBUG] workers stopped
[DEBUG] consumer stopped
`
	assert.Equal(t, expected, l.Logs())

	assert.GreaterOrEqual(t, countPublished, uint32(200))
	assert.LessOrEqual(t, countPublished, uint32(520))

	assert.GreaterOrEqual(t, countConsumed, uint32(200))
	assert.LessOrEqual(t, countConsumed, uint32(520))
}
