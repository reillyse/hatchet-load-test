package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/joho/godotenv"

	"github.com/hatchet-dev/hatchet/pkg/client"
	"github.com/hatchet-dev/hatchet/pkg/client/types"
	"github.com/hatchet-dev/hatchet/pkg/cmdutils"
	"github.com/hatchet-dev/hatchet/pkg/worker"
)

type loadTestEvent struct {
	Data map[string]string `json:"data"`
}

type stepOneOutput struct {
	Message string `json:"message"`
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Default().Println("Error loading .env file")
	}

	interrupt := cmdutils.InterruptChan()
	c, err := client.New()

	if err != nil {
		panic(err)
	}

	cleanup, err := run(c)
	if err != nil {
		panic(err)
	}

	testEvent := loadTestEvent{

		Data: map[string]string{
			"test": "test",
		},
	}

	log.Printf("pushing event hatchet:load-test")
	// push an event
	// err = c.Event().Push(
	// 	context.Background(),
	// 	"hatchet:ha:load-test:v2",
	// 	testEvent,
	// 	client.WithEventMetadata(map[string]string{
	// 		"hello": "loadtest",
	// 	}),
	// )

	// start workflow run

	wid, err := c.Admin().RunWorkflow(
		"ha-loadtester-v3",
		&testEvent,
	)

	if err != nil {
		panic(fmt.Errorf("error pushing event: %w", err))
	}

	log.Printf("workflow run started: %s \n", wid.WorkflowRunId())

	sig := <-interrupt

	log.Printf("received interrupt signal %s shutting down \n", sig)

	if err := cleanup(); err != nil {
		panic(fmt.Errorf("error cleaning up: %w", err))
	}
}

func run(c client.Client) (func() error, error) {

	w, err := worker.NewWorker(
		worker.WithClient(
			c,
		),
	)
	if err != nil {
		return nil, fmt.Errorf("error creating worker: %w", err)
	}

	// we need to retry the workflow if it is unavailable
	retryCount := 10
	for {

		err = w.RegisterWorkflow(
			&worker.WorkflowJob{
				On:          worker.Events("hatchet:ha:load-test:v2"),
				Name:        "ha-loadtester-v3",
				Description: "Run a load test",
				Concurrency: worker.Expression("'default'").MaxRuns(1).LimitStrategy(types.GroupRoundRobin),
				Steps: []*worker.WorkflowStep{
					worker.Fn(func(ctx worker.HatchetContext) (result *stepOneOutput, err error) {
						input := &loadTestEvent{}

						err = ctx.WorkflowInput(input)

						if err != nil {
							return nil, err
						}

						log.Printf("start")
						timeStart := time.Now()

						events := os.Getenv("HATCHET_LOADTEST_EVENTS")
						duration := os.Getenv("HATCHET_LOADTEST_DURATION")

						if events == "" {
							events = "1"
						}

						if duration == "" {
							duration = "10s"
						}

						if input.Data["duration"] != "" {
							duration = input.Data["duration"]
							log.Printf("using duration from input data: %s", duration)
						}

						if input.Data["events"] != "" {
							events = input.Data["events"]
							log.Printf("using events from input data: %s", events)
						}

						// for container it is in root
						testCmd := fmt.Sprintf("/loadtest loadtest --duration \"%s\" --events %s", duration, events)
						//testCmd := "echo \"Hello, World!\""
						// run a load test
						commandCtx, cancel := context.WithCancel(context.Background())
						defer cancel()

						cmd := exec.CommandContext(commandCtx, "sh", "-c", testCmd)

						// Inherit the current environment
						cmd.Env = os.Environ()

						// Set the output to go to the standard output and error
						cmd.Stdout = os.Stdout
						cmd.Stderr = os.Stderr

						// Run the command
						err = cmd.Run()

						if err != nil {
							fmt.Println("Error running command " + testCmd)
							fmt.Println(err)
							return nil, err
						}
						timeEnd := time.Now()

						timeTaken := timeEnd.Sub(timeStart)
						fmt.Println("Time taken: ", timeTaken)
						return &stepOneOutput{Message: fmt.Sprintf("%s", timeTaken)}, nil

					},
					).SetName("step-one").SetRetries(0),
				},
			},
		)

		if err == nil {
			break
		}

		if retryCount > 0 {
			retryCount--
			time.Sleep(5 * time.Second)
			log.Printf("got error %s retrying %d more times", err, retryCount)
		} else {
			break
		}

		if err != nil && retryCount == 0 {
			return nil, fmt.Errorf("error registering workflow: %w", err)
		}
	}

	cleanup, err := w.Start()
	if err != nil {
		panic(err)
	}

	return cleanup, nil
}
