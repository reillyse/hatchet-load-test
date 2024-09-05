package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/joho/godotenv"

	"github.com/hatchet-dev/hatchet/pkg/client"
	"github.com/hatchet-dev/hatchet/pkg/cmdutils"
	"github.com/hatchet-dev/hatchet/pkg/worker"
)

type userCreateEvent struct {
	Username string            `json:"username"`
	UserID   string            `json:"user_id"`
	Data     map[string]string `json:"data"`
}

type stepOneOutput struct {
	Message string `json:"message"`
}

func main() {
	err := godotenv.Load()
	if err != nil {
		panic(err)
	}

	// get the number of events to push from args to the cli
	var eventsArg string
	if len(os.Args) > 1 {
		eventsArg = os.Args[1]
	}

	var numEvents int

	if eventsArg == "" {
		numEvents = 5000000
	} else {
		numEvents, err = strconv.Atoi(eventsArg)
		if err != nil {
			panic(err)
		}
	}

	var workerCountArg string
	if len(os.Args) > 1 {
		workerCountArg = os.Args[2]
	}

	var numWorkers int

	if workerCountArg == "" {
		numWorkers = 5
	} else {
		numWorkers, err = strconv.Atoi(workerCountArg)
		if err != nil {
			panic(err)
		}
	}

	interrupt := cmdutils.InterruptChan()
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	workerCount := numWorkers
	wg.Add(workerCount)

	go func() {
		for i := range workerCount {
			go func(i int) {
				_, err = run(ctx, numEvents/workerCount, i)
				if err != nil {
					panic(err)
				}
				wg.Done()

			}(i)
		}

		wg.Wait()
		cancel()
	}()

	select {
	case <-interrupt:
		return
	case <-ctx.Done():
		return

	}

}

func getConcurrencyKey(ctx worker.HatchetContext) (string, error) {
	return "user-create", nil
}

func run(ctx context.Context, numEvents int, workerId int) (func() error, error) {
	c, err := client.New()

	if err != nil {
		return nil, fmt.Errorf("error creating client: %w", err)
	}

	fmt.Printf("worker %v: loading %v events\n", workerId, numEvents)

	for i := 0; i < numEvents; i++ {
		select {
		case <-ctx.Done():
			return nil, nil
		default:

			if (i % 1000) == 0 {
				fmt.Printf("worker %v: loaded %v events\n", workerId, i)
			}

			testEvent := userCreateEvent{
				Username: "echo-test",
				UserID:   "1234",
				Data: map[string]string{
					"test": "test",
				},
			}

			// log.Printf("pushing event user:create:load_test")
			// push an event
			err := c.Event().Push(
				context.Background(),
				"user:create:load_test",
				testEvent,
				client.WithEventMetadata(map[string]string{
					"hello": "load test",
				}),
			)
			if err != nil {
				panic(fmt.Errorf("error pushing event: %w", err))
			}

		}

	}
	fmt.Printf("worker %v: loaded %v events\n", workerId, numEvents)

	return nil, nil
}
