package main

import (
	"context"
	"embed"

	"github.com/codefly-dev/core/agents/services"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/standards"
	"github.com/codefly-dev/core/wool"
)

type Builder struct {
	*services.DefaultBuilder
	*Service
}

func NewBuilder() *Builder {
	service := NewService()
	return &Builder{
		DefaultBuilder: services.NewDefaultBuilder(service.Builder),
		Service:        service,
	}
}

func (s *Builder) Load(ctx context.Context, req *builderv0.LoadRequest) (*builderv0.LoadResponse, error) {
	defer s.Wool.Catch()

	return s.Builder.LoadService(ctx, req, services.BuilderLoad{
		Settings:         s.Settings,
		Requirements:     requirements,
		FactoryTemplates: factoryFS,
		ResolveEndpoints: func(ctx context.Context, endpoints []*basev0.Endpoint) error {
			endpoint, err := resources.FindTCPEndpoint(ctx, endpoints)
			if err != nil {
				return err
			}
			s.TcpEndpoint = endpoint
			s.Wool.Debug("endpoint", wool.Field("tcp", endpoint))
			return nil
		},
	})
}

func (s *Builder) Deploy(ctx context.Context, req *builderv0.DeploymentRequest) (*builderv0.DeploymentResponse, error) {
	defer s.Wool.Catch()
	s.Base.SetDockerImage(image)

	return s.Builder.DeployKustomize(ctx, req, services.KustomizeDeployment{
		EnvironmentVariables: s.EnvironmentVariables,
		Templates:            deploymentFS,
		Prepare: func(ctx context.Context, deployment *services.KustomizeDeploymentContext) error {
			instance, err := resources.FindNetworkInstanceInNetworkMappings(ctx, req.GetNetworkMappings(), s.TcpEndpoint, resources.NewPublicNetworkAccess())
			if err != nil {
				return err
			}
			configuration, err := s.CreateConnectionConfiguration(ctx, req.GetConfiguration(), instance, false)
			if err != nil {
				return err
			}
			return deployment.ExportConfiguration(ctx, configuration)
		},
	})
}

type create struct {
}

func (s *Builder) Create(ctx context.Context, req *builderv0.CreateRequest) (*builderv0.CreateResponse, error) {
	defer s.Wool.Catch()

	c := create{}

	err := s.Templates(ctx, c, services.WithFactory(factoryFS))
	if err != nil {
		return s.Builder.CreateError(err)
	}

	err = s.CreateEndpoints(ctx)
	if err != nil {
		return s.Builder.CreateErrorf(err, "cannot create endpoints")
	}

	s.Wool.Debug("created endpoints", wool.Field("endpoints", resources.MakeManyEndpointSummary(s.Endpoints)))

	return s.Builder.CreateResponse(ctx, s.Settings)
}

func (s *Builder) CreateEndpoints(ctx context.Context) error {
	tcp, err := resources.LoadTCPAPI(ctx)
	if err != nil {
		return s.Wool.Wrapf(err, "cannot load tcp api")
	}
	endpoint := s.Base.BaseEndpoint(standards.TCP)
	endpoint.Visibility = resources.VisibilityExternal
	s.TcpEndpoint, err = resources.NewAPI(ctx, endpoint, resources.ToTCPAPI(tcp))
	s.Endpoints = []*basev0.Endpoint{s.TcpEndpoint}
	return nil
}

//go:embed templates/factory
var factoryFS embed.FS

//go:embed templates/deployment
var deploymentFS embed.FS
