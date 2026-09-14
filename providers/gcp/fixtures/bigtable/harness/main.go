// This loopback-only fixture translates table REST requests to Google's
// unmodified bttest gRPC server. It never supplies table metadata itself.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	bt "cloud.google.com/go/bigtable/admin/apiv2/adminpb"
	"cloud.google.com/go/bigtable/bttest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "loopback HTTP address")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || host != "127.0.0.1" {
		log.Fatal("listen must use 127.0.0.1")
	}
	emulator, err := bttest.NewServer("127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer emulator.Close()
	conn, err := grpc.NewClient(emulator.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	client := bt.NewBigtableTableAdminClient(conn)
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/projects/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v2/"), "/")
		if len(parts) < 5 || len(parts) > 6 || parts[0] != "projects" || parts[2] != "instances" || parts[4] != "tables" {
			http.NotFound(w, r)
			return
		}
		for _, part := range parts {
			if part == "" || part == "." || part == ".." {
				http.Error(w, "invalid path", 400)
				return
			}
		}
		parent := strings.Join(parts[:4], "/")
		name := strings.Join(parts, "/")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		decode := func(msg proto.Message) error {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				return err
			}
			return protojson.Unmarshal(raw, msg)
		}
		view := bt.Table_VIEW_UNSPECIFIED
		if v := r.URL.Query().Get("view"); v != "" {
			n, ok := bt.Table_View_value[v]
			if !ok {
				http.Error(w, "unknown view", 400)
				return
			}
			view = bt.Table_View(n)
		}
		var result proto.Message
		var err error
		switch {
		case len(parts) == 5 && r.Method == "GET":
			size := 0
			if raw := r.URL.Query().Get("pageSize"); raw != "" {
				size, err = strconv.Atoi(raw)
				if err != nil || size < 0 || size > 2147483647 {
					http.Error(w, "invalid pageSize", 400)
					return
				}
			}
			result, err = client.ListTables(ctx, &bt.ListTablesRequest{Parent: parent, View: view, PageSize: int32(size), PageToken: r.URL.Query().Get("pageToken")})
		case len(parts) == 5 && r.Method == "POST":
			req := &bt.CreateTableRequest{}
			if err = decode(req); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			req.Parent = parent
			result, err = client.CreateTable(ctx, req)
		case len(parts) == 6 && r.Method == "GET":
			result, err = client.GetTable(ctx, &bt.GetTableRequest{Name: name, View: view})
		case len(parts) == 6 && r.Method == "DELETE":
			result, err = client.DeleteTable(ctx, &bt.DeleteTableRequest{Name: name})
		case len(parts) == 6 && r.Method == "PATCH":
			table := &bt.Table{}
			if err = decode(table); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if table.Name != "" && table.Name != name {
				http.Error(w, "table identity mismatch", 400)
				return
			}
			table.Name = name
			mask := &fieldmaskpb.FieldMask{}
			encoded, _ := json.Marshal(r.URL.Query().Get("updateMask"))
			if err = protojson.Unmarshal(encoded, mask); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			result, err = client.UpdateTable(ctx, &bt.UpdateTableRequest{Table: table, UpdateMask: mask})
		default:
			http.Error(w, "unsupported fixture method", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			code := 500
			switch status.Code(err) {
			case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
				code = 400
			case codes.NotFound:
				code = 404
			case codes.AlreadyExists, codes.Aborted:
				code = 409
			case codes.PermissionDenied:
				code = 403
			case codes.Unauthenticated:
				code = 401
			case codes.ResourceExhausted:
				code = 429
			case codes.Unimplemented:
				code = 501
			case codes.Unavailable:
				code = 503
			case codes.DeadlineExceeded:
				code = 504
			case codes.Canceled:
				code = 499
			}
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": status.Convert(err).Message()}})
			return
		}
		raw, err := protojson.Marshal(result)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Write(raw)
	})
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	fmt.Println("http://" + listener.Addr().String())
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Print(err)
	}
}
