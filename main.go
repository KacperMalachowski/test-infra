package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"k8s.io/test-infra/prow/config"
)

type configs struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
	Image string `json:"image"`
}

func main() {
	prowConfig, err := config.Load("prow/config.yaml", "prow/jobs", nil, "")
	if err != nil {
		log.Fatalf("failed to load prow job config: %s", err)
	}

	mapa := map[string][]configs{}

	for _, jobs := range prowConfig.PresubmitsStatic {
		for _, job := range jobs {
			if strings.Contains(job.Name, "-build") {
				continue
			}
			owner := job.Annotations["owner"]
			image := job.Spec.Containers[0].Image

			mapa[owner] = append(mapa[owner], configs{
				Name:  job.Name,
				Image: image,
				Owner: owner,
			})
		}
	}

	for _, jobs := range prowConfig.PostsubmitsStatic {
		for _, job := range jobs {
			if strings.Contains(job.Name, "-build") {
				continue
			}
			owner := job.Annotations["owner"]
			image := job.Spec.Containers[0].Image

			mapa[owner] = append(mapa[owner], configs{
				Name:  job.Name,
				Image: image,
				Owner: owner,
			})
		}
	}

	for _, job := range prowConfig.Periodics {
		if strings.Contains(job.Name, "-build") {
			continue
		}
		owner := job.Annotations["owner"]
		image := job.Spec.Containers[0].Image

		mapa[owner] = append(mapa[owner], configs{
			Name:  job.Name,
			Image: image,
			Owner: owner,
		})

	}

	data, _ := json.MarshalIndent(mapa, "  ", "")
	os.Mkdir("data", os.ModePerm)
	os.WriteFile("data/jobs.json", data, os.ModePerm)
	fmt.Println("Count of teams that should migrate: ", len(mapa))
	fmt.Print("Teams:")
	for key, jobs := range mapa {
		fmt.Print(" ", key)
		data, _ := json.MarshalIndent(jobs, "  ", "")
		os.WriteFile(fmt.Sprintf("data/jobs_%s.json", key), data, os.ModePerm)
	}
}
