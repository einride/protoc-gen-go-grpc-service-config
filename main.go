package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"sync"

	serviceconfigv1 "go.buf.build/protocolbuffers/go/einride/grpc-service-config/einride/serviceconfig/v1"
	"go.buf.build/protocolbuffers/go/grpc/grpc/grpc/service_config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

const docURL = "https://github.com/grpc/grpc/blob/master/doc/service_config.md"

func main() {
	var (
		flags    flag.FlagSet
		path     = flags.String("path", "", "input path of service config JSON files")
		validate = flags.Bool("validate", false, "validate service configs")
		required = flags.Bool("required", false, "require every service to have a service config")
	)
	protogen.Options{
		ParamFunc: flags.Set,
	}.Run(func(gen *protogen.Plugin) error {
		p, err := newPlugin(gen, *path)
		if err != nil {
			return err
		}
		if *validate {
			if err := p.validate(*required); err != nil {
				return err
			}
		}
		if err := p.generateFromJSON(); err != nil {
			return err
		}
		return p.generateFromProto()
	})
}

type plugin struct {
	gen   *protogen.Plugin
	files *protoregistry.Files
	path  string
}

func newPlugin(gen *protogen.Plugin, path string) (*plugin, error) {
	var files protoregistry.Files
	for _, file := range gen.Files {
		if err := files.RegisterFile(file.Desc); err != nil {
			return nil, err
		}
	}
	return &plugin{
		gen:   gen,
		path:  path,
		files: &files,
	}, nil
}

func (p *plugin) generateFromProto() error {
	for _, file := range p.gen.Files {
		if !file.Generate {
			continue
		}
		defaultServiceConfig := proto.GetExtension(
			file.Proto.GetOptions(),
			serviceconfigv1.E_DefaultServiceConfig,
		).(*service_config.ServiceConfig)
		if defaultServiceConfig == nil {
			continue
		}
		g := p.gen.NewGeneratedFile(
			filepath.Dir(file.GeneratedFilenamePrefix)+
				"/"+string(file.Desc.Package().Parent().Name())+
				"_grpc_service_config.pb.go",
			file.GoImportPath,
		)
		g.P("// Code generated by protoc-gen-go-grpc-service-config. DO NOT EDIT.")
		g.P("package ", file.GoPackageName)
		g.P()
		g.P("// DefaultServiceConfig is the default service config for all services in the package.")
		g.P("// Source: ", file.Desc.Path(), ".")
		g.P("const DefaultServiceConfig = `", protojson.MarshalOptions{}.Format(defaultServiceConfig), "`")
	}
	return nil
}

func (p *plugin) generateFromJSON() error {
	generatedServiceConfigFiles := map[string]struct{}{}
	for _, file := range p.gen.Files {
		if !file.Generate {
			continue
		}
		for _, service := range file.Services {
			serviceConfigFile := p.resolveServiceConfigJSONFile(service)
			if _, err := os.Stat(serviceConfigFile); err != nil {
				continue
			}
			if _, ok := generatedServiceConfigFiles[serviceConfigFile]; ok {
				continue
			}
			generatedServiceConfigFiles[serviceConfigFile] = struct{}{}
			data, err := ioutil.ReadFile(serviceConfigFile)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &serviceConfigJSON{}); err != nil {
				return fmt.Errorf("run: invalid service config file %s: %w", serviceConfigFile, err)
			}
			g := p.gen.NewGeneratedFile(
				filepath.Dir(file.GeneratedFilenamePrefix)+"/"+filepath.Base(serviceConfigFile)+".go",
				file.GoImportPath,
			)
			g.P("// Code generated by protoc-gen-go-grpc-service-config. DO NOT EDIT.")
			g.P("package ", file.GoPackageName)
			g.P()
			g.P("// ServiceConfig is the service config for all services in the package.")
			g.P("// Source: ", filepath.Base(serviceConfigFile), ".")
			g.P("const ServiceConfig = `", string(data), "`")
		}
	}
	return nil
}

func (p *plugin) resolveServiceConfigJSONFile(service *protogen.Service) string {
	parentPackageName := string(service.Desc.ParentFile().Package().Parent().Name())
	fileName := parentPackageName + "_grpc_service_config.json"
	fullyQualifiedFileName := filepath.Join(p.path, filepath.Dir(service.Location.SourceFile), fileName)
	return fullyQualifiedFileName
}

func (p *plugin) resolveServiceConfigFromJSONFile(service *protogen.Service) (string, bool, error) {
	serviceConfigJSONFile := p.resolveServiceConfigJSONFile(service)
	if _, err := os.Stat(p.resolveServiceConfigJSONFile(service)); err == nil {
		serviceConfigJSON, err := os.ReadFile(serviceConfigJSONFile)
		if err != nil {
			return "", false, fmt.Errorf("resolve %s service config: %w", service.Desc.FullName(), err)
		}
		return string(serviceConfigJSON), true, nil
	}
	return "", false, nil
}

func (p *plugin) resolveServiceConfigFromFileAnnotation(service *protogen.Service) (string, bool, error) {
	var serviceConfig *service_config.ServiceConfig
	p.files.RangeFilesByPackage(service.Desc.ParentFile().Package(), func(file protoreflect.FileDescriptor) bool {
		serviceConfig = proto.GetExtension(
			file.Options(),
			serviceconfigv1.E_DefaultServiceConfig,
		).(*service_config.ServiceConfig)
		return serviceConfig == nil
	})
	if serviceConfig == nil {
		return "", false, nil
	}
	return protojson.Format(serviceConfig), true, nil
}

func (p *plugin) resolveServiceConfig(service *protogen.Service) (string, bool, error) {
	fromJSON, ok, err := p.resolveServiceConfigFromJSONFile(service)
	if err != nil {
		return "", false, err
	}
	if ok {
		return fromJSON, true, nil
	}
	return p.resolveServiceConfigFromFileAnnotation(service)
}

func (p *plugin) validate(required bool) error {
	addr, cleanup, err := p.startLocalServer()
	if err != nil {
		return err
	}
	defer cleanup()
	for _, file := range p.gen.Files {
		if !file.Generate {
			continue
		}
		for _, service := range file.Services {
			serviceConfig, ok, err := p.resolveServiceConfig(service)
			if err != nil {
				return err
			}
			if !ok && required {
				return fmt.Errorf(
					"validate: missing service config for %s (see: %s)",
					service.Desc.FullName(),
					docURL,
				)
			}
			// gRPC Go validates a service config when dialing.
			conn, err := grpc.Dial(
				addr,
				grpc.WithDefaultServiceConfig(serviceConfig),
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithBlock(),
			)
			if err != nil {
				return fmt.Errorf("validate: invalid service config for %s: %w", service.Desc.FullName(), err)
			}
			if err := conn.Close(); err != nil {
				return err
			}
			var serviceConfigContent serviceConfigJSON
			if err := json.Unmarshal([]byte(serviceConfig), &serviceConfigContent); err != nil {
				return err
			}
			if required && !serviceConfigContent.hasService(service) {
				return fmt.Errorf(
					"validate: missing service config for %s (see: %s)",
					service.Desc.FullName(),
					docURL,
				)
			}
		}
	}
	return nil
}

type serviceConfigJSON struct {
	MethodConfigs []struct {
		Names []struct {
			Service string
			Method  string
		} `json:"name"`
	} `json:"methodConfig"`
}

func (c serviceConfigJSON) hasService(service *protogen.Service) bool {
	for _, methodConfig := range c.MethodConfigs {
		for _, name := range methodConfig.Names {
			if (name.Service == "" && name.Method == "") ||
				(name.Service == string(service.Desc.FullName()) && name.Method == "") {
				return true
			}
		}
	}
	return false
}

func (p *plugin) startLocalServer() (string, func(), error) {
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return "", nil, err
	}
	localServer := grpc.NewServer()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = localServer.Serve(lis)
	}()
	cleanup := func() {
		localServer.Stop()
		wg.Wait()
	}
	return lis.Addr().String(), cleanup, nil
}
