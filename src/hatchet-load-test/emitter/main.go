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

	cleanup, err := run()
	if err != nil {
		panic(err)
	}

	<-interrupt

	if err := cleanup(); err != nil {
		panic(fmt.Errorf("error cleaning up: %w", err))
	}
}

func run() (func() error, error) {
	c, err := client.New()

	if err != nil {
		return nil, fmt.Errorf("error creating client: %w", err)
	}

	w, err := worker.NewWorker(
		worker.WithClient(
			c,
		),
	)
	if err != nil {
		return nil, fmt.Errorf("error creating worker: %w", err)
	}

	err = w.RegisterWorkflow(
		&worker.WorkflowJob{
			On:          worker.Events("hatchet:load-test"),
			Name:        "load",
			Description: "Run a load test",
			Steps: []*worker.WorkflowStep{
				worker.Fn(func(ctx worker.HatchetContext) (result *stepOneOutput, err error) {
					input := &loadTestEvent{}

					err = ctx.WorkflowInput(input)

					if err != nil {
						return nil, err
					}

					log.Printf("start")
					timeStart := time.Now()

					testCmd := "./loadtest loadtest --duration \"20s\" --events 10"
					// run a load test

					cmd := exec.Command("sh", "-c", testCmd)

					// Inherit the current environment
					cmd.Env = os.Environ()

					// Set the output to go to the standard output and error
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr

					// Run the command
					err = cmd.Run()

					if err != nil {
						return nil, err
					}
					timeEnd := time.Now()

					timeTaken := timeEnd.Sub(timeStart)

					return &stepOneOutput{Message: fmt.Sprintf("%s", timeTaken)}, nil
				},
				).SetName("step-one"),
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("error registering workflow: %w", err)
	}

	go func() {
		testEvent := loadTestEvent{

			Data: map[string]string{
				"test": "test",
			},
		}

		log.Printf("pushing event hatchet:load-test")
		// push an event
		err := c.Event().Push(
			context.Background(),
			"hatchet:load-test",
			testEvent,
			client.WithEventMetadata(map[string]string{
				"hello": "loadtest",
			}),
		)
		if err != nil {
			panic(fmt.Errorf("error pushing event: %w", err))
		}
	}()

	cleanup, err := w.Start()
	if err != nil {
		panic(err)
	}

	return cleanup, nil
}
