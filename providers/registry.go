package providers

import "strings"

var (
	providers = make(map[string]Provider)
)

func Register(p Provider) {
	providers[p.Name()] = p
}

func ListProviders() []Provider {
	var result []Provider
	for _, p := range providers {
		result = append(result, p)
	}
	return result
}

func GetProvider(name string) (Provider, bool) {
	p, ok := providers[name]
	return p, ok
}

func FindModel(model string) (Provider, string, bool) {
	if strings.Contains(model, "/") {
		parts := strings.SplitN(model, "/", 2)
		if p, ok := providers[parts[0]]; ok {
			return p, parts[1], true
		}
		return nil, "", false
	}
	for _, p := range providers {
		if p.IsConfigured() {
			for _, m := range p.Models() {
				if m == model {
					return p, model, true
				}
			}
		}
	}
	for _, p := range providers {
		for _, m := range p.FreeModels() {
			if m == model {
				return p, model, true
			}
		}
	}
	return nil, "", false
}
