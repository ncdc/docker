package graph

import (
	"github.com/docker/docker/archive"
	"github.com/docker/docker/engine"
	"github.com/docker/docker/image"
)

func (s *TagStore) CmdSquash(job *engine.Job) engine.Status {
	if len(job.Args) != 2 {
		return job.Errorf("Not enough arguments. Usage: %s BASEIMAGE IMAGE\n", job.Name)
	}

	base, err := s.LookupImage(job.Args[0])
	if err != nil {
		return job.Errorf("No such image: %s", job.Args[0])
	}
	leaf, err := s.LookupImage(job.Args[1])
	if err != nil {
		return job.Errorf("No such image: %s", job.Args[1])
	}

	foundBase := false
	leaf.WalkHistory(func(img *image.Image) error {
		if img.ID == base.ID {
			foundBase = true
		}
		return nil
	})

	if !foundBase || base == leaf {
		return job.Errorf("%s is not decendant from %s", job.Args[1], job.Args[0])
	}

	comment := job.Getenv("comment")
	if comment == "" {
		comment = "Squashed from " + leaf.ID
	}

	basePath, err := s.graph.driver.Get(base.ID, "")
	if err != nil {
		return job.Error(err)
	}
	defer s.graph.driver.Put(base.ID)

	leafPath, err := s.graph.driver.Get(leaf.ID, "")
	if err != nil {
		return job.Error(err)
	}
	defer s.graph.driver.Put(leaf.ID)

	changes, err := archive.ChangesDirs(leafPath, basePath)
	if err != nil {
		return job.Error(err)
	}

	archive, err := archive.ExportChanges(leafPath, changes)
	if err != nil {
		return job.Error(err)
	}
	defer archive.Close()

	config := leaf.Config
	// Make a copy of the config in case it is modified
	if config != nil {
		c := *config
		config = &c
	}

	img, err := s.graph.Create(archive, "", base.ID, comment, leaf.Author, nil, config)
	if err != nil {
		return job.Error(err)
	}

	// Register the image if needed
	repository := job.Getenv("repo")
	if repository != "" {
		tag := job.Getenv("tag")
		if err := s.Set(repository, tag, img.ID, true); err != nil {
			return job.Error(err)
		}
	}
	if err != nil {
		return job.Error(err)
	}
	job.Printf("%s\n", img.ID)
	return engine.StatusOK
}
