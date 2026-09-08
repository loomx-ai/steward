package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
)

func main() {
	providerName := flag.String("provider", "", "cloud provider: alicloud, aws, gcp, or azure")
	format := flag.String("format", "openapi", "official metadata format")
	sourcePath := flag.String("source", "", "path to official metadata")
	outputPath := flag.String("output", "", "path for deterministic generated catalog")
	flag.Parse()

	if err := run(asset.Provider(*providerName), *format, *sourcePath, *outputPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(provider asset.Provider, format, sourcePath, outputPath string) error {
	if provider == "" || sourcePath == "" || outputPath == "" {
		return fmt.Errorf("provider, source, and output are required")
	}
	if provider != asset.ProviderAliCloud && provider != asset.ProviderAWS && provider != asset.ProviderGCP && provider != asset.ProviderAzure {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read official metadata: %w", err)
	}
	generated, err := catalog.ImportOfficial(format, provider, sourcePath, source)
	if err != nil {
		return err
	}
	payload, err := catalog.MarshalGenerated(generated)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, payload, 0o644); err != nil {
		return fmt.Errorf("write generated catalog: %w", err)
	}
	return nil
}
