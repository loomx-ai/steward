// Harness for the pinned, unmodified Google mockgcp Monitoring implementation.
// Run inside that repository's mockgcp module; this is not a Steward dependency.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/GoogleCloudPlatform/k8s-config-connector/mockgcp/common"
	"github.com/GoogleCloudPlatform/k8s-config-connector/mockgcp/common/operations"
	"github.com/GoogleCloudPlatform/k8s-config-connector/mockgcp/common/projects"
	"github.com/GoogleCloudPlatform/k8s-config-connector/mockgcp/mockbilling"
	"github.com/GoogleCloudPlatform/k8s-config-connector/mockgcp/mockbillingbudgets"
	"github.com/GoogleCloudPlatform/k8s-config-connector/mockgcp/mocklogging"
	"github.com/GoogleCloudPlatform/k8s-config-connector/mockgcp/mockmonitoring"
	"github.com/GoogleCloudPlatform/k8s-config-connector/mockgcp/pkg/storage"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"strings"
)

type fixtureProjects struct{}

func (fixtureProjects) GetProjectByIDOrNumber(value string) (*projects.ProjectData, error) {
	for id, number := range map[string]int64{"sample-project": 123456, "monitored-project": 222222, "foreign-project": 987654} {
		if value == id || value == strconv.FormatInt(number, 10) {
			return &projects.ProjectData{ID: id, Number: number}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "fixture project not found")
}
func (p fixtureProjects) GetProjectByID(value string) (*projects.ProjectData, error) {
	return p.GetProjectByIDOrNumber(value)
}
func (p fixtureProjects) GetProjectByNumber(value string) (*projects.ProjectData, error) {
	return p.GetProjectByIDOrNumber(value)
}
func (p fixtureProjects) GetProject(value *projects.ProjectName) (*projects.ProjectData, error) {
	if value.ProjectID != "" {
		return p.GetProjectByID(value.ProjectID)
	}
	return p.GetProjectByNumber(strconv.FormatInt(value.ProjectNumber, 10))
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	data := storage.NewInMemoryStorage()
	service := mockmonitoring.New(&common.MockEnvironment{Projects: fixtureProjects{}}, data)
	grpcServer := grpc.NewServer()
	service.Register(grpcServer)
	loggingService := mocklogging.New(&common.MockEnvironment{Projects: fixtureProjects{}}, data)
	loggingService.Register(grpcServer)
	billingService := mockbilling.New(&common.MockEnvironment{Projects: fixtureProjects{}}, data)
	billingService.Register(grpcServer)
	budgetService := mockbillingbudgets.New(&common.MockEnvironment{Projects: fixtureProjects{}}, data)
	budgetService.Register(grpcServer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	defer grpcServer.Stop()
	go grpcServer.Serve(listener)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	serviceMux, err := service.NewHTTPMux(ctx, conn)
	if err != nil {
		log.Fatal(err)
	}
	opMux := runtime.NewServeMux()
	if err := operations.NewOperationsService(data).RegisterOperationsPath("/v1/operations/{name}")(ctx, opMux, conn); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/operations/", opMux)
	loggingMux, err := loggingService.NewHTTPMux(ctx, conn)
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/v2/", loggingMux)
	billingMux, err := billingService.NewHTTPMux(ctx, conn)
	if err != nil {
		log.Fatal(err)
	}
	budgetMux, err := budgetService.NewHTTPMux(ctx, conn)
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/v1/billingAccounts", billingMux)
	mux.Handle("/v1/billingAccounts/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/budgets") {
			budgetMux.ServeHTTP(w, r)
		} else {
			billingMux.ServeHTTP(w, r)
		}
	}))
	mux.Handle("/", serviceMux)
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Handler: mux}
	defer server.Close()
	fmt.Println("http://" + httpListener.Addr().String())
	go server.Serve(httpListener)
	<-ctx.Done()
}
