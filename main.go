package main

import (
	"context"
	"embed"
	"fmt"
	"github.com/codefly-dev/core/agents"
	"github.com/codefly-dev/core/agents/services"
	"github.com/codefly-dev/core/builders"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	"github.com/codefly-dev/core/resources"
	runnersbase "github.com/codefly-dev/core/runners/base"
	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/templates"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

// Agent version
var agent = shared.Must(resources.LoadFromFs[resources.Agent](shared.Embed(infoFS)))

var requirements = builders.NewDependencies(agent.Name,
	builders.NewDependency("service.codefly.yaml"),
)

type Settings struct {
}

const HotReload = "hot-reload"
const DatabaseName = "database-name"

var image = &resources.DockerImage{
	Name:   "amazon/dynamodb-local",
	Tag:    "3.3.0",
	Digest: "sha256:d89f8fcc6b1a39cb35976c248ed42a28c66ae00dc043099210f5571e42648ab4",
}

type Service struct {
	*services.Base

	// Settings
	*Settings

	Region      string
	TcpEndpoint *basev0.Endpoint
}

func (s *Service) GetAgentInformation(ctx context.Context, _ *agentv0.AgentInformationRequest) (*agentv0.AgentInformation, error) {

	readme, err := templates.ApplyTemplateFrom(ctx, shared.Embed(readmeFS), "templates/agent/README.md", s.Information)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return services.Advertisement{
		Backends: runnersbase.BackendSupport{
			Docker: true,
		},
		Config: []*agentv0.ConfigurationValueDetail{
			{
				Name: "dynamodb", Description: "dynamodb credentials",
				Fields: []*agentv0.ConfigurationValueInformation{
					{
						Name: "region", Description: "AWS region",
					},
					{

						Name: "aws-profile", Description: "AWS profile",
					},
				},
			},
		},
		ReadMe: readme,
	}.Build(), nil
}

func (s *Service) CreateConnectionConfiguration(ctx context.Context, conf *basev0.Configuration, instance *basev0.NetworkInstance, withSSL bool) (*basev0.Configuration, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	if instance == nil || instance.GetAddress() == "" {
		return nil, s.Wool.NewError("dynamodb network instance is missing an address")
	}
	endpoint := instance.GetAddress()
	if !strings.Contains(endpoint, "://") {
		endpoint = fmt.Sprintf("http://%s", endpoint)
	}
	region, err := resources.GetConfigurationValue(ctx, conf, "dynamodb", "AWS_REGION")
	if err != nil {
		return nil, err
	}
	if region == "" {
		region = "us-east-1"
	}

	outputConf := &basev0.Configuration{
		Origin:         s.Base.Unique(),
		RuntimeContext: resources.RuntimeContextFromInstance(instance),
		Infos: []*basev0.ConfigurationInformation{
			{
				Name: "dynamodb",
				ConfigurationValues: []*basev0.ConfigurationValue{
					{Key: "endpoint", Value: endpoint},
					{Key: "region", Value: region},
				},
			},
		},
	}
	return outputConf, nil
}

func NewService() *Service {
	return &Service{
		Base:     services.NewServiceBase(context.Background(), agent.Of(resources.ServiceAgent)),
		Settings: &Settings{},
	}
}

func main() {
	agents.Serve(agents.PluginRegistration{
		Agent:   NewService(),
		Runtime: NewRuntime(),
		Builder: NewBuilder(),
	})
}

//go:embed agent.codefly.yaml
var infoFS embed.FS

//go:embed templates/agent
var readmeFS embed.FS
