package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"

	"github.com/hatchet-dev/hatchet/pkg/client"
	"github.com/hatchet-dev/hatchet/pkg/client/rest"
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

	interrupt := cmdutils.InterruptChan()
	var cleanup func() error

	numWorkers := 5
	var wg sync.WaitGroup
	wg.Add(numWorkers)
	ctx := context.Background()
	cleanupAll := make([]func() error, numWorkers)
	go func() {
		for i := range numWorkers {

			go func() {
				cleanup, err = run(ctx, i)
				if err != nil {
					panic(err)
				}
				cleanupAll[i] = cleanup
				wg.Done()
			}()

		}
		wg.Wait()
		log.Println("workers all started")
	}()

	checkClient, err := client.New()
	if err != nil {
		panic(fmt.Errorf("error creating second client: %w", err))
	}

	// not quite sure how to filter by the exact event id - it takes a UUID I have a string
	checkParams := rest.WorkflowRunGetMetricsParams{}

	startDepth, err := checkQueueDepth(ctx, checkClient, checkParams)

	if err != nil {
		panic(fmt.Errorf("error checking queue depth: %w", err))
	}

	var newQueueDepth = -1

	drainSeconds := 60

OuterLoop:
	for {

		select {

		case <-time.After(time.Duration(drainSeconds) * time.Second):
			newQueueDepth, err = checkQueueDepth(ctx, checkClient, checkParams)
			if err != nil {
				panic(fmt.Errorf("error checking queue depth: %w", err))
			}

			break OuterLoop

		case <-interrupt:

			if cleanup != nil {

				if err := cleanup(); err != nil {
					panic(fmt.Errorf("error cleaning up: %w", err))
				}
			}

			break OuterLoop
		}

	}

	for _, cleanup := range cleanupAll {
		if cleanup != nil {
			if err := cleanup(); err != nil {
				panic(fmt.Errorf("error cleaning up: %w", err))
			}
		}
		cleanup()
	}

	if newQueueDepth > -1 {

		log.Printf("queue depth went from %d to %d", startDepth, newQueueDepth)

		queueDiff := startDepth - newQueueDepth

		throughput := float64(queueDiff) / float64(drainSeconds)

		log.Printf("throughput: %f / second", throughput)
	}

	os.Exit(0)

}

func checkQueueDepth(ctx context.Context, checkClient client.Client, checkParams rest.WorkflowRunGetMetricsParams) (int, error) {
	resp, err := checkClient.API().WorkflowRunGetMetricsWithResponse(ctx, uuid.MustParse(checkClient.TenantId()), &checkParams)
	if err != nil {
		panic(fmt.Errorf("error getting metrics: %w", err))
	}

	response := resp.JSON200.Counts

	if response == nil {
		panic(fmt.Errorf("no counts in response"))
	}
	if response.PENDING == nil {
		panic(fmt.Errorf("no pending in response"))
	}
	if response.QUEUED == nil {
		panic(fmt.Errorf("no queued in response"))
	}

	log.Printf("response: %+v \n", *response)

	queueDepth := *response.PENDING + *response.QUEUED + *response.RUNNING

	log.Printf("queue depth: %d", queueDepth)

	return queueDepth, err
}

func getConcurrencyKey(ctx worker.HatchetContext) (string, error) {
	return "user-create", nil
}

func run(ctx context.Context, workerId int) (func() error, error) {
	c, err := client.New(client.WithLogLevel("warn"))
	log.Printf("worker %v started \n", workerId)

	if err != nil {
		return nil, fmt.Errorf("error creating client: %w", err)
	}

	w, err := worker.NewWorker(
		worker.WithClient(
			c,
		),
		worker.WithLogLevel("warn"),
	)
	if err != nil {
		return nil, fmt.Errorf("error creating worker: %w", err)
	}

	testSvc := w.NewService("test")

	err = testSvc.RegisterWorkflow(
		&worker.WorkflowJob{
			On:              worker.Events("user:create:load_test"),
			Name:            "load_test",
			Description:     "This runs to test the speed of draining.",
			ScheduleTimeout: "8760h",
			Steps: []*worker.WorkflowStep{
				worker.Fn(func(ctx worker.HatchetContext) (result *stepOneOutput, err error) {
					input := &userCreateEvent{}

					err = ctx.WorkflowInput(input)

					if err != nil {
						return nil, err
					}

					// log.Printf("step-one")

					return &stepOneOutput{
						Message: "Username is: " + input.Username,
					}, nil
				},
				).SetName("step-one"),
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("error registering workflow: %w", err)
	}

	cleanup, err := w.Start()

	fmt.Println("worker started .................")
	if err != nil {
		panic(err)
	}

	return cleanup, nil
}
