package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
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

type childInput struct {
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
	testEvent := loadTestEvent{

		Data: map[string]string{
			"test": "test",
		},
	}
	interrupt := cmdutils.InterruptChan()
	c, err := client.New()

	if err != nil {
		panic(err)
	}
	go func() {
		time.Sleep(5 * time.Second) //just to let the worker get registered

		log.Printf("pushing event hatchet:load-test")
		wid, err := c.Admin().RunWorkflow(
			"ha-loadtester-v3",
			&testEvent,
		)

		if err != nil {
			panic(fmt.Errorf("error pushing event: %w", err))
		}

		log.Printf("workflow run started: %s \n", wid.WorkflowRunId())
	}()

	cleanup, err := run(c)
	if err != nil {
		panic(err)
	}

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
		worker.WithMaxRuns(1000),
	)
	if err != nil {
		return nil, fmt.Errorf("error creating worker: %w", err)
	}

	err = w.RegisterWorkflow(
		&worker.WorkflowJob{
			On:          worker.NoTrigger(),
			Name:        "child-workflow",
			Description: "Run a child workflow",
			Steps: []*worker.WorkflowStep{
				worker.Fn(func(ctx worker.HatchetContext) (result *stepOneOutput, err error) {
					input := &childInput{}

					err = ctx.WorkflowInput(input)

					log.Printf("child workflow started with input: %v", input)

					if err != nil {
						return nil, err
					}

					return &stepOneOutput{Message: "child workflow"}, nil
				},
				).SetName("step-one").SetRetries(0),
			},
		},
	)

	if err != nil {
		return nil, fmt.Errorf("error registering child workflow: %w", err)
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

						workflows := os.Getenv("HATCHET_LOADTEST_WORKFLOW_RUNS")

						if workflows == "" {
							workflows = "10000"
						}

						if input.Data["workflows"] != "" {
							workflows = input.Data["workflows"]
							log.Printf("using workflows from input data: %s", workflows)
						}

						var pushConcurrency int

						if input.Data["pushConcurrency"] != "" {
							pc := input.Data["pushConcurrency"]

							pushConcurrency, err = strconv.Atoi(pc)
							if err != nil {
								return nil, fmt.Errorf("error converting pushConcurrency to int: %w", err)
							}

							log.Printf("using pushConcurrency from input data: %d", pushConcurrency)
						}

						if pushConcurrency == 0 {
							pushConcurrency = 100
						}

						workflowCount, err := strconv.ParseInt(workflows, 10, 32)
						if err != nil {
							return nil, fmt.Errorf("error converting workflows to int64: %w", err)
						}

						start := time.Now()

						var wg sync.WaitGroup
						results := make([]string, workflowCount)
						resultCh := make(chan string, workflowCount)

						semChannel := make(chan struct{}, pushConcurrency)

						for i := 0; i < int(workflowCount); i++ {
							wg.Add(1)
							go func(i int) {
								fmt.Println("waiting for semaphore")
								semChannel <- struct{}{}

								if i%100 == 0 {
									fmt.Printf("spawning  %d th workflow \n", i)
								}

								defer wg.Done()
								childInput := childInput{Data: map[string]string{"in": strconv.Itoa(i)}}
								childWorkflow, err := ctx.SpawnWorkflow("child-workflow", childInput, &worker.SpawnWorkflowOpts{})
								if err != nil {
									// Handle error here
									panic(err)

								}
								<-semChannel
								// Collect the result from the child workflow
								result, err := childWorkflow.Result()
								if err != nil {
									panic(err)

								}
								fmt.Println(result)

								resultCh <- "result-" + strconv.Itoa(i) + ": "
							}(i)
						}
						go func() {

							wg.Wait()
							fmt.Println("awaited all of the workflows")
							close(resultCh) // this unblocks the range
						}()

						// Collect all results
						for result := range resultCh {
							results = append(results, result)
						}

						// wait for all child workflows to complete

						parsedDuration := time.Since(start)

						durationSeconds := parsedDuration.Seconds()

						eventsPerSecond := float64(workflowCount) / durationSeconds

						return &stepOneOutput{Message: fmt.Sprintf("%.2f workflows per second", eventsPerSecond)}, nil

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
