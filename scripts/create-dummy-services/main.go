package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/utils/ptr"
)

var (
	kubeconfig *string
	namespace  *string
	count      *int
	workers    *int
)

func main() {
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	namespace = flag.String("namespace", "default", "namespace for the services")
	count = flag.Int("count", 10000, "number of dummy services to create")
	workers = flag.Int("workers", 50, "number of parallel workers (API call concurrency)")
	flag.Parse()

	fmt.Printf("Creating %d dummy services with %d parallel workers...\n", *count, *workers)
	startTime := time.Now()

	// Build Kubernetes client
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// Increase rate limits for high-throughput operations
	// Default QPS=5 is too low for parallel service creation
	config.QPS = 100   // requests per second
	config.Burst = 200 // burst capacity

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	// Create work queue
	jobs := make(chan int, *count)
	results := make(chan error, *count)
	var wg sync.WaitGroup

	// Start worker goroutines
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go worker(w, clientset, jobs, results, &wg)
	}

	// Send jobs
	for i := 1; i <= *count; i++ {
		jobs <- i
	}
	close(jobs)

	// Wait for completion
	wg.Wait()
	close(results)

	// Check results
	successCount := 0
	errorCount := 0
	for err := range results {
		if err == nil {
			successCount++
		} else {
			errorCount++
			fmt.Printf("Error: %v\n", err)
		}
	}

	duration := time.Since(startTime)
	fmt.Printf("\n============================================\n")
	fmt.Printf(" Creation Complete in %v\n", duration)
	fmt.Printf("============================================\n")
	fmt.Printf("Successfully created: %d services\n", successCount)
	fmt.Printf("Errors: %d\n", errorCount)
	fmt.Printf("\n")
	fmt.Printf("Verifying from API server:\n")

	// Verify count
	svcList, err := clientset.CoreV1().Services(*namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: "type=dummy",
	})
	if err == nil {
		fmt.Printf("Found %d services with label type=dummy\n", len(svcList.Items))
	}

	fmt.Printf("\n")
	fmt.Printf("Diagnostic commands:\n")
	fmt.Printf("  1. Check kube-proxy logs:\n")
	fmt.Printf("     kubectl -n kube-system logs -l k8s-app=kube-proxy --tail=20\n")
	fmt.Printf("  2. Count iptables rules (on node):\n")
	fmt.Printf("     sudo iptables -t nat -L KUBE-SERVICES --line-numbers -n | tail -n +3 | wc -l\n")
	fmt.Printf("  3. Verify EndpointSlice:\n")
	fmt.Printf("     kubectl get endpointslices -l kubernetes.io/service-name=dummy-service-1\n")
	fmt.Printf("\n")
}

func worker(id int, clientset *kubernetes.Clientset, jobs <-chan int, results chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()

	for i := range jobs {
		err := createServiceWithEndpoint(clientset, i)
		results <- err

		// Progress indicator every 1000 services
		if i%1000 == 0 {
			fmt.Printf("Worker %d: Created %d services...\n", id, i)
		}
	}
}

func createServiceWithEndpoint(clientset *kubernetes.Clientset, index int) error {
	serviceName := fmt.Sprintf("dummy-service-%d", index)

	// Use TEST-NET-1 IP range (192.0.2.0/24), cycling through 1-254
	fakeIP := fmt.Sprintf("192.0.2.%d", ((index-1)%254)+1)

	// Create Service (no selector - allows manual endpoints)
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: *namespace,
			Labels: map[string]string{
				"type": "dummy",
			},
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeClusterIP,
			Ports: []v1.ServicePort{{
				Port:       80,
				TargetPort: intstr.FromInt(80),
				Protocol:   v1.ProtocolTCP,
			}},
		},
	}

	_, err := clientset.CoreV1().Services(*namespace).Create(context.Background(), svc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create service %s: %w", serviceName, err)
	}

	// Create EndpointSlice (modern API, preferred by kube-proxy)
	eps := &discovery.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: *namespace,
			Labels: map[string]string{
				discovery.LabelServiceName: serviceName,
				"type":                     "dummy",
			},
		},
		AddressType: discovery.AddressTypeIPv4,
		Endpoints: []discovery.Endpoint{
			{
				Addresses: []string{fakeIP},
				Conditions: discovery.EndpointConditions{
					Ready: ptr.To(true),
				},
			},
		},
		Ports: []discovery.EndpointPort{
			{
				Port:     ptr.To(int32(80)),
				Protocol: ptr.To(v1.ProtocolTCP),
			},
		},
	}

	_, err = clientset.DiscoveryV1().EndpointSlices(*namespace).Create(context.Background(), eps, metav1.CreateOptions{})
	if err != nil {
		// Clean up service if endpoint creation fails
		clientset.CoreV1().Services(*namespace).Delete(context.Background(), serviceName, metav1.DeleteOptions{})
		return fmt.Errorf("failed to create endpointslice for %s: %w", serviceName, err)
	}

	return nil
}
