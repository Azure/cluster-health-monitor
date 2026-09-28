package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	resourceName = "nvidia.com/gpu"
	endpoint     = "chm-fake-nvidia.sock"
	deviceCount  = 8
)

type plugin struct {
	pluginapi.UnimplementedDevicePluginServer
	devices []*pluginapi.Device
}

func newPlugin() *plugin {
	devices := make([]*pluginapi.Device, 0, deviceCount)
	for i := range deviceCount {
		devices = append(devices, &pluginapi.Device{
			ID:     fmt.Sprintf("fake-gpu-%d", i),
			Health: pluginapi.Healthy,
		})
	}
	return &plugin{devices: devices}
}

func (p *plugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{}, nil
}

func (p *plugin) ListAndWatch(_ *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	if err := stream.Send(&pluginapi.ListAndWatchResponse{Devices: p.devices}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}

func (p *plugin) Allocate(_ context.Context, request *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	response := &pluginapi.AllocateResponse{}
	for _, containerRequest := range request.ContainerRequests {
		response.ContainerResponses = append(response.ContainerResponses, &pluginapi.ContainerAllocateResponse{
			Envs: map[string]string{
				"NVIDIA_VISIBLE_DEVICES": strings.Join(containerRequest.DevicesIDs, ","),
			},
		})
	}
	return response, nil
}

func (p *plugin) GetPreferredAllocation(context.Context, *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	return &pluginapi.PreferredAllocationResponse{}, nil
}

func (p *plugin) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}

func dialUnix(ctx context.Context, socket string) (*grpc.ClientConn, error) {
	return grpc.DialContext(ctx, "unix://"+socket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}),
	)
}

func main() {
	socket := filepath.Join(pluginapi.DevicePluginPath, endpoint)
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		log.Fatalf("remove stale socket: %v", err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		log.Fatalf("listen on %s: %v", socket, err)
	}
	defer listener.Close()

	server := grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(server, newPlugin())
	go func() {
		if err := server.Serve(listener); err != nil {
			log.Fatalf("serve device plugin: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	self, err := dialUnix(ctx, socket)
	if err != nil {
		log.Fatalf("wait for plugin server: %v", err)
	}
	self.Close()

	kubelet, err := dialUnix(ctx, pluginapi.KubeletSocket)
	if err != nil {
		log.Fatalf("connect to kubelet: %v", err)
	}
	defer kubelet.Close()
	_, err = pluginapi.NewRegistrationClient(kubelet).Register(ctx, &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     endpoint,
		ResourceName: resourceName,
	})
	if err != nil {
		log.Fatalf("register device plugin: %v", err)
	}
	log.Printf("registered %d fake devices for %s", deviceCount, resourceName)

	select {}
}
