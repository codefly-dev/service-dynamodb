package main

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/codefly-dev/core/agents/helpers/code"

	"github.com/codefly-dev/core/wool"

	"github.com/codefly-dev/core/agents/services"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/codefly-dev/core/resources"
	dockerrun "github.com/codefly-dev/core/runners/dockerrun"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/lib/pq"
)

type Runtime struct {
	services.RuntimeServer
	*Service

	// internal
	runnerEnvironment *dockerrun.DockerEnvironment

	dynamodbPort uint16
	localPort    uint32
}

func NewRuntime() *Runtime {
	return &Runtime{
		Service: NewService(),
	}
}

func (s *Runtime) Load(ctx context.Context, req *runtimev0.LoadRequest) (*runtimev0.LoadResponse, error) {
	defer s.Wool.Catch()

	return s.Runtime.LoadService(ctx, req, services.RuntimeLoad{
		Settings:     s.Settings,
		Requirements: requirements,
		ResolveEndpoints: func(ctx context.Context, endpoints []*basev0.Endpoint) error {
			s.Wool.Debug("endpoints", wool.Field("endpoints", resources.MakeManyEndpointSummary(endpoints)))
			endpoint, err := resources.FindTCPEndpoint(ctx, endpoints)
			if err != nil {
				return s.Wool.Wrapf(err, "cannot find TCP endpoint")
			}
			s.TcpEndpoint = endpoint
			return nil
		},
	})
}

func (s *Runtime) Init(ctx context.Context, req *runtimev0.InitRequest) (*runtimev0.InitResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Runtime.LogInitRequest(req)
	s.Runtime.WithContext(req.GetRuntimeContext())

	w := s.Wool.In("runtime::init")

	s.NetworkMappings = req.ProposedNetworkMappings

	s.Configuration = req.Configuration

	// Get region
	region, err := resources.GetConfigurationValue(ctx, s.Configuration, "dynamodb", "AWS_REGION")
	if err != nil {
		return s.Runtime.InitError(err)
	}
	if region == "" {
		region = "us-east-1"
	}
	s.Region = region

	net, err := resources.FindNetworkMapping(ctx, s.NetworkMappings, s.TcpEndpoint)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	if net == nil {
		return s.Runtime.InitError(w.NewError("network mapping is nil"))
	}

	instance, err := resources.FindNetworkInstanceInNetworkMappings(ctx, s.NetworkMappings, s.TcpEndpoint, s.Runtime.NetworkAccess())
	if err != nil {
		return s.Runtime.InitError(err)
	}

	if instance == nil {
		return s.Runtime.InitError(w.NewError("network instance is nil"))
	}

	s.localPort = instance.Port
	w.Debug("tcp network instance", wool.Field("instance", instance))

	s.Infof("will run on %s", instance.Host)
	s.dynamodbPort = 8000

	// Create connection configurations resources for the network instance
	for _, inst := range net.Instances {
		conf, errConn := s.CreateConnectionConfiguration(ctx, s.Configuration, inst, false)
		if errConn != nil {
			return s.Runtime.InitError(errConn)
		}
		w.Debug("adding configuration", wool.Field("config", resources.MakeConfigurationSummary(conf)), wool.Field("instance", inst))
		s.Runtime.RuntimeConfigurations = append(s.Runtime.RuntimeConfigurations, conf)
	}
	s.Wool.Debug("sending runtime configuration", wool.Field("conf", resources.MakeManyConfigurationSummary(s.Runtime.RuntimeConfigurations)))

	w.Debug("setting up connection string for migrations")

	// Docker
	runner, err := dockerrun.NewDockerHeadlessEnvironment(ctx, image, s.UniqueWithWorkspace())
	if err != nil {
		return s.Runtime.InitError(err)
	}

	runner.WithOutput(s.Wool)
	runner.WithPortMapping(ctx, uint16(instance.Port), s.dynamodbPort)

	s.runnerEnvironment = runner

	w.Debug("init for runner environment: will start container")
	err = s.runnerEnvironment.Init(ctx)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	s.Wool.Debug("init successful")
	return s.Runtime.InitResponse()
}

func CreateLocalClient(region string, endpoint string) *dynamodb.Client {
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
		config.WithEndpointResolver(aws.EndpointResolverFunc(
			func(service, region string) (aws.Endpoint, error) {
				return aws.Endpoint{URL: endpoint}, nil
			})),
		config.WithCredentialsProvider(credentials.StaticCredentialsProvider{
			Value: aws.Credentials{
				AccessKeyID: "dummy", SecretAccessKey: "dummy", SessionToken: "dummy",
				Source: "Hard-coded credentials; values are irrelevant for local DynamoDB",
			},
		}),
	)
	if err != nil {
		panic(err)
	}

	return dynamodb.NewFromConfig(cfg)
}

func (s *Runtime) WaitForReady(ctx context.Context) error {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Wool.Debug("waiting for ready")

	client := CreateLocalClient(s.Region, fmt.Sprintf("http://localhost:%d", s.localPort))

	maxRetry := 5
	for retry := 0; retry < maxRetry; retry++ {
		// Check if DynamoDB is ready by listing tables
		_, err := client.ListTables(ctx, &dynamodb.ListTablesInput{})
		if err == nil {
			s.Wool.Debug("DynamoDB is ready!")
			return nil
		}
		s.Wool.Debug("waiting for database to be ready")
		time.Sleep(3 * time.Second)
	}
	return s.Wool.NewError("database is not ready")
}

func (s *Runtime) Start(ctx context.Context, req *runtimev0.StartRequest) (*runtimev0.StartResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Wool.Debug("starting")

	s.Wool.Debug("waiting for ready")

	err := s.WaitForReady(ctx)
	if err != nil {
		return s.Runtime.StartError(err)
	}

	s.Wool.Debug("start done")
	return s.Runtime.StartResponse()
}

func (s *Runtime) Information(ctx context.Context, req *runtimev0.InformationRequest) (*runtimev0.InformationResponse, error) {
	return s.Runtime.InformationResponse(ctx, req)
}

func (s *Runtime) Stop(ctx context.Context, req *runtimev0.StopRequest) (*runtimev0.StopResponse, error) {
	defer s.Wool.Catch()

	s.Wool.Debug("nothing to stop: keep environment alive")

	return s.Runtime.StopResponse()
}

func (s *Runtime) Destroy(ctx context.Context, req *runtimev0.DestroyRequest) (*runtimev0.DestroyResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Wool.Debug("Destroying")

	// Get the runner environment
	runner, err := dockerrun.NewDockerHeadlessEnvironment(ctx, image, s.UniqueWithWorkspace())
	if err != nil {
		return s.Runtime.DestroyError(err)
	}

	err = runner.Shutdown(ctx)
	if err != nil {
		return s.Runtime.DestroyError(err)
	}
	return s.Runtime.DestroyResponse()
}

func (s *Runtime) Test(ctx context.Context, req *runtimev0.TestRequest) (*runtimev0.TestResponse, error) {
	return s.Runtime.TestResponse()
}

/* Details

 */

func (s *Runtime) EventHandler(event code.Change) error {
	return nil
}
