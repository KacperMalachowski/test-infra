package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/test-infra/prow/config"
)

type configs struct {
	Name  string `yaml:"name"`
	Owner string `yaml:"owner"`
	Image string `yaml:"image"`
}

func main() {
	testInfraPath := os.Args[0]

	if !strings.HasSuffix(testInfraPath, "/") {
		testInfraPath = fmt.Sprintf("%s/", testInfraPath)
	}

	prowConfig, err := config.Load(testInfraPath+"prow/config.yaml", testInfraPath+"prow/jobs", nil, "")
	if err != nil {
		log.Fatalf("failed to load prow job config: %s", err)
	}

	mapa := map[string][]configs{}

	for _, jobs := range prowConfig.PresubmitsStatic {
		for _, job := range jobs {
			if strings.Contains(job.Name, "-build") || strings.Contains(job.Spec.Containers[0].Image, "image-builder") {
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
			if strings.Contains(job.Spec.Containers[0].Image, "image-builder") {
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
		if strings.Contains(job.Spec.Containers[0].Image, "image-builder") {
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

	data, _ := yaml.Marshal(mapa)
	os.Mkdir("data", os.ModePerm)
	os.WriteFile("data/jobs.yaml", data, os.ModePerm)
	fmt.Println("Count of teams that should migrate: ", len(mapa))
	fmt.Print("Teams:")
	for key, jobs := range mapa {
		fmt.Print(" ", key)
		data, _ := yaml.Marshal(jobs)
		os.WriteFile(fmt.Sprintf("data/jobs_%s.yaml", key), data, os.ModePerm)
	}
}
