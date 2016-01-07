package graph

import (
	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution"
	"github.com/docker/distribution/registry/api/errcode"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/registry"
	"golang.org/x/net/context"
)

type v2TagLister struct {
	*TagStore
	endpoint registry.APIEndpoint
	config   *RemoteTagsConfig
	repoInfo *registry.RepositoryInfo
	repo     distribution.Repository
}

func (p *v2TagLister) ListTags() (tagList []*types.RepositoryTag, fallback bool, err error) {
	authConfig := registry.ResolveAuthConfigFromMap(p.config.AuthConfigs, p.repoInfo.Index)
	p.repo, err = NewV2Repository(p.repoInfo, p.endpoint, p.config.MetaHeaders, &authConfig)
	if err != nil {
		logrus.Debugf("Error getting v2 registry: %v", err)
		return nil, true, err
	}

	tagList, err = p.listTagsWithRepository()
	if err != nil && registry.ContinueOnError(err) {
		logrus.Debugf("Error trying v2 registry: %v", err)
		fallback = true
	}
	return
}

func (p *v2TagLister) listTagsWithRepository() ([]*types.RepositoryTag, error) {
	logrus.Debugf("Retrieving the tag list from V2 endpoint %v", p.endpoint.URL)
	manSvc, err := p.repo.Manifests(context.Background())
	if err != nil {
		return nil, err
	}
	tags, err := manSvc.Tags()
	if err != nil {
		switch t := err.(type) {
		case errcode.Errors:
			if len(t) == 1 {
				return nil, t[0]
			}
		}
		return nil, err
	}
	tagList := make([]*types.RepositoryTag, len(tags))
	for i, tag := range tags {
		tagList[i] = &types.RepositoryTag{Tag: tag}
	}
	return tagList, nil
}