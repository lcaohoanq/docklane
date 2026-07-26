package traefik

import (
	"fmt"
	"strings"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

type Configuration struct {
	HTTP HTTPConfiguration `json:"http"`
}

type HTTPConfiguration struct {
	Routers  map[string]Router  `json:"routers"`
	Services map[string]Service `json:"services"`
}

type Router struct {
	Rule        string   `json:"rule"`
	EntryPoints []string `json:"entryPoints"`
	Service     string   `json:"service"`
	TLS         TLS      `json:"tls"`
}

type TLS struct{}

type Service struct {
	LoadBalancer LoadBalancer `json:"loadBalancer"`
}

type LoadBalancer struct {
	Servers []Server `json:"servers"`
}

type Server struct {
	URL string `json:"url"`
}

func Build(routes []domain.Route, containers []docker.Container, baseDomain string) Configuration {
	config := Configuration{
		HTTP: HTTPConfiguration{
			Routers:  map[string]Router{},
			Services: map[string]Service{},
		},
	}
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		container, err := docker.ResolveContainer(route.Selector, containers)
		if err != nil {
			continue
		}
		if err := docker.ValidateTCPPort(container, route.Port); err != nil {
			continue
		}
		name := safeName(route.Name)
		config.HTTP.Routers[name] = Router{
			Rule:        fmt.Sprintf("Host(`%s`)", route.Hostname(baseDomain)),
			EntryPoints: []string{"websecure"},
			Service:     name,
			TLS:         TLS{},
		}
		config.HTTP.Services[name] = Service{
			LoadBalancer: LoadBalancer{
				Servers: []Server{{
					URL: fmt.Sprintf("%s://%s:%d", route.Scheme, container.Name, route.Port),
				}},
			},
		}
	}
	return config
}

func safeName(value string) string {
	return strings.ReplaceAll(value, ".", "-")
}
