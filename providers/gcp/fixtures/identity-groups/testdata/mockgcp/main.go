// Harness for the pinned, unmodified Google mockgcp Cloud Identity implementation.
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
	"syscall"

	"github.com/GoogleCloudPlatform/k8s-config-connector/mockgcp/common"
	"github.com/GoogleCloudPlatform/k8s-config-connector/mockgcp/mockcloudidentity"
	"github.com/GoogleCloudPlatform/k8s-config-connector/mockgcp/pkg/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	data := storage.NewInMemoryStorage()
	service := mockcloudidentity.New(&common.MockEnvironment{}, data)
	grpcServer := grpc.NewServer()
	service.Register(grpcServer)
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
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Handler: serviceMux}
	defer server.Close()
	fmt.Println("http://" + httpListener.Addr().String())
	go server.Serve(httpListener)
	<-ctx.Done()
}
