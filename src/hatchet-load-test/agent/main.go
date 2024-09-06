package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"google.golang.org/api/option"
	"google.golang.org/api/sqladmin/v1"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
)

func main() {

	db := configurePostgres()

	deleteEphemeralHatchet()
	setupEphemeralHatchet(db)

}

func configurePostgres() hatchetDB {
	instance := &sqladmin.DatabaseInstance{
		DatabaseVersion: "POSTGRES_16",
		InstanceType:    "CLOUD_SQL_INSTANCE",
		Name:            "hatchet-stack-ephemeral-instance",
		Project:         "hatchet-staging",
		Region:          "us-west1",

		Settings: &sqladmin.Settings{
			Tier:           "db-perf-optimized-N-2",
			DataDiskSizeGb: 10,
			DataDiskType:   "PD_SSD",
			BackupConfiguration: &sqladmin.BackupConfiguration{
				Enabled: true, // Enable backups
			},
			ActivationPolicy: "ALWAYS", // Instance is always on
		},
	}

	ctx := context.Background()
	// either grab a postgres dump from somewhere or set it up

	if os.Getenv("POSTGRES_DUMP") != "" {
		// restore from dump
	} else {

		// check the google account being used

		// create a new postgres instance
		serviceAccountKey := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")

		sqladminService, err := sqladmin.NewService(ctx, option.WithCredentialsFile(serviceAccountKey))
		if err != nil {
			log.Fatalf("Error creating service: %v", err)
		}

		log.Println("Service created")

		// lets see if we can list

		instances, err := sqladminService.Instances.List("hatchet-staging").Do()
		if err != nil {
			log.Fatalf("Error listing instances: %v", err)
		}
		for _, instance := range instances.Items {
			log.Printf("Instance: %v", instance.Name)
		}

		if contains(instances.Items, "hatchet-stack-ephemeral-instance") {
			log.Println("Instance already exists")

		} else {
			//should wait for this to complete (operationID)
			res, err := sqladminService.Instances.Insert("hatchet-staging", instance).Do()
			if err != nil {
				if res != nil {
					log.Printf("Error creating instance: %v", res.Error)
				}
				log.Fatalf("Error creating instance: %v", err)
			}

			log.Println(res)

		}

		list, err := sqladminService.Databases.List("hatchet-staging", instance.Name).Context(ctx).Do()
		if err != nil {
			log.Fatalf("Error listing databases: %v", err)
		}

		ephemeralDB := containsDB(list.Items, "hatchet-stack-ephemeral-db")
		if ephemeralDB != nil {

			log.Println("Database already exists")

			log.Printf("Database already exists Name: %v Selflink: %v  \n", ephemeralDB.Name, ephemeralDB.SelfLink)

		} else {

			ops, err := sqladmin.NewDatabasesService(sqladminService).Insert("hatchet-staging", instance.Name, &sqladmin.Database{
				Name: "hatchet-stack-ephemeral-db",
			}).Do()

			if err != nil {
				log.Fatalf("Error creating database: %v", err)
			}

			log.Printf("Database created: %v", ops)
			ephemeralDB, err = sqladmin.NewDatabasesService(sqladminService).Get("hatchet-staging", instance.Name, "hatchet-stack-ephemeral-db").Do()
			if err != nil {
				log.Fatalf("Error getting database: %v", err)
			}

			log.Printf("Database created Name: %v Host: %v  \n", ephemeralDB.Name, ephemeralDB.SelfLink)
		}

	}
	getDatabaseURL(instance)

	return hatchetDB{
		URL:      getDatabaseURL(instance),
		Host:     instance.ConnectionName,
		Database: "hatchet-stack-ephemeral-db",
		Port:     "5432",
		User:     "hatchet",
		Password: "hatchet",
	}

}

func getDatabaseURL(instance *sqladmin.DatabaseInstance) string {
	// DATABASE_URL='postgresql://hatchet:hatchet@127.0.0.1:5431/hatchet'

	var host string
	if len(instance.IpAddresses) > 0 {
		host = instance.IpAddresses[0].IpAddress
	} else {
		host = instance.ConnectionName
	}

	port := 5432

	return fmt.Sprintf("postgresql://hatchet:hatchet@%s:%s/hatchet", host, port)

}

// func getConnectionString(){
// 	POSTGRESS_URI=postgresql:///dbname
//   ?host=/cloudsql/myprojectid:region:myinstanceid
//   &user=username
//   &password=password
//   &sslmode=disable
// }

func contains(s []*sqladmin.DatabaseInstance, e string) bool {
	for _, a := range s {
		if a.Name == e {
			return true
		}
	}
	return false
}

func containsDB(s []*sqladmin.Database, e string) *sqladmin.Database {
	for _, a := range s {
		if a.Name == e {
			return a
		}
	}
	return nil
}

//cloudSqlSidecar

type hatchetDB struct {
	URL      string
	Host     string
	Port     string
	User     string
	Password string
	Database string
}

func setupEphemeralHatchet(db hatchetDB) {

	// need to pass the DB url to the helm chart somehow (maybe values)
	// then need to read back the hatchet connection string and pass it to the individual hatchet runs
	// although could just use a known connection string for the hatchet runs

	settings := cli.New()
	settings.SetNamespace("hatch-stack-ephemeral-ns")

	actionConfig := new(action.Configuration)

	if err := actionConfig.Init(settings.RESTClientGetter(), "hatch-stack-ephemeral-ns", os.Getenv("HELM_DRIVER"), log.Printf); err != nil {
		log.Printf("%+v", err)
		os.Exit(1)
	}

	client := action.NewInstall(actionConfig)

	aimChart := "hatchet/hatchet-stack"

	cp, err := client.ChartPathOptions.LocateChart(aimChart, settings)

	client.DryRun = false
	client.ReleaseName = "hatchet-stack"
	client.Namespace = "hatch-stack-ephemeral-ns"
	client.Wait = true
	client.Timeout = 10 * time.Minute
	client.WaitForJobs = true

	client.Replace = true

	if err != nil {
		log.Fatalf("Error locating chart %s: %s", aimChart, err)
	}

	chartRequested, err := loader.Load(cp)
	if err != nil {
		log.Fatalf("Error loading chart %s: %s", aimChart, err)
	}

	vals := map[string]interface{}{}

	valuesJson := `{
		"api": {
			"env": {
				"DATABASE_URL": "` + db.URL + `",
				"DATABASE_POSTGRES_HOST": "` + db.Host + `",
				"DATABASE_POSTGRES_PORT": "` + db.Port + `",
				"DATABASE_POSTGRES_USER": "` + db.User + `",
				"DATABASE_POSTGRES_PASSWORD": "` + db.Password + `",
				"DATABASE_POSTGRES_DB_NAME": "` + db.Database + `",
				"DATABASE_POSTGRES_MODE": "disable"
			}
		},
		"engine": {
			"env": {
				"DATABASE_URL": "` + db.URL + `",
				"DATABASE_POSTGRES_HOST": "` + db.Host + `",
				"DATABASE_POSTGRES_PORT": "` + db.Port + `",
				"DATABASE_POSTGRES_USER": "` + db.User + `",
				"DATABASE_POSTGRES_PASSWORD": "` + db.Password + `",
				"DATABASE_POSTGRES_DB_NAME": "` + db.Database + `",
				"DATABASE_POSTGRES_MODE": "disable"
			}
		}
	}`
	err = json.Unmarshal([]byte(valuesJson), &vals)
	if err != nil {
		log.Fatalf("error unmarshaling JSON: %v", err)
	}

	log.Printf("Values %+v \n", vals)

	// value overwrites

	// caddy enabled to true

	// port forward to caddy engine for port forward 8080 to the api
	//api.env

	//engine.env

	// add a deployment replacing the sidecar that postgres can connect to
	// if can connect directly

	release, err := client.Run(chartRequested, vals)
	if err != nil {
		log.Fatalf("Error installing chart %s: %s", aimChart, err)

	}
	fmt.Printf("The values are %+v \n", vals)

	fmt.Printf("Release %s has been installed. Happy Hatcheting! \n", release.Name)

	fmt.Printf("Release Info :\n %+v", release.Info)
}

// helm uninstall

func deleteEphemeralHatchet() {

	settings := cli.New()
	settings.SetNamespace("hatch-stack-ephemeral-ns")

	actionConfig := new(action.Configuration)

	if err := actionConfig.Init(settings.RESTClientGetter(), "hatch-stack-ephemeral-ns", os.Getenv("HELM_DRIVER"), log.Printf); err != nil {
		log.Printf("%+v", err)
		os.Exit(1)
	}

	client := action.NewUninstall(actionConfig)

	client.DryRun = false

	client.Timeout = 10 * time.Minute

	// helm uninstall hatchet-stack --namespace hatch-stack-ephemeral-ns

	_, err := client.Run("hatchet-stack")
	if err != nil {
		log.Fatalf("Error uninstalling chart %s: %s", "hatchet-stack", err)
	}

	fmt.Printf("Release %s has been uninstalled. Happy UnHatcheting! \n", "hatchet-stack")

}
