package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"net/http"
	"bytes"
	"io"
	"net"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	// Imported to understand GKE authentication directives in kubeconfig file
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	"github.com/awalterschulze/gographviz"
	"encoding/json"
	restclient "k8s.io/client-go/rest"
	"net/url"
	"strings"
	"regexp"
	"encoding/binary"
	"strconv"
)

func execute(method string, url *url.URL, config *restclient.Config, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
	exec, err := remotecommand.NewSPDYExecutor(config, method, url)

	if err != nil {
		return err
	}

	return exec.Stream(remotecommand.StreamOptions{
		Stdin:              stdin,
		Stdout:             stdout,
		Stderr:             stderr,
		Tty:                tty,
	})
}

func GenDiagram(pods *v1.PodList) *gographviz.Escape {
	nodeLastPod := make(map[string]string)

	// Add a graph that will escape the required characters
	graph := gographviz.NewEscape()
	graph.SetName("kube")
	graph.SetDir(true)
	for _, pod := range pods.Items {
		clustername := "cluster_"+pod.Spec.NodeName

		// It's okay to add the same sub graph and the same label. There will not be any duplicate
		graph.AddSubGraph("kube",clustername,map[string]string{"style":"dashed","color":"blue"})
		graph.AddAttr(clustername,"label",pod.Spec.NodeName)

		// Set the color depending on the Namespace
		color := "blue"
		if pod.Namespace == "kube-system" {
			color = "red"
		}
		// Set the shape depending on the owner resource of the pod (daemonset, replicaset ...)
		// TODO: handle multiple owner references
		shape := "rectangle"
		if len(pod.OwnerReferences) > 0 {
			if pod.OwnerReferences[0].Kind == "ReplicaSet" {
				shape = "oval"
			} else if pod.OwnerReferences[0].Kind == "DaemonSet" {
				shape = "diamond"
			}
		}
		// Add the graph node
		graph.AddNode(clustername,pod.Name,map[string]string{"shape":shape,"color":color})

		// Add the edges to have proper alignment
		// We use a map to store the last pod added for this node
		if nodeLastPod[pod.Spec.NodeName] != "" {
			graph.AddEdge(nodeLastPod[pod.Spec.NodeName],pod.Name,true,map[string]string{"style":"invisible","dir":"none"})
		}
		nodeLastPod[pod.Spec.NodeName] = pod.Name
	}
	return graph
}

func getKubeConfig() (*restclient.Config,error) {
	var kubeconfig *string

	// Get home dir
	home := os.Getenv("HOME")
	if home == "" {
		home = os.Getenv("USERPROFILE") // windows
	}

	if home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	// use the current context in kubeconfig
	return clientcmd.BuildConfigFromFlags("", *kubeconfig)
}

type kubeAPIHandler struct {
	kubeConfig *restclient.Config
	clientSet *kubernetes.Clientset
}

func int2ip(nn uint32) net.IP {
	ip := make(net.IP, 4)
	binary.LittleEndian.PutUint32(ip, nn)
	return ip
}

// Get the TCP Connections from proc fs
func (k *kubeAPIHandler) GetConn(procfile string,protocol string,ipversion int,namespace string, podName string, containerName string) ([]connectionDesc, error) {
	req := k.clientSet.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		Param("container", containerName)
	req.VersionedParams(&v1.PodExecOptions{
		Container: "",
		Command:   []string{"cat","/proc/net/" + procfile},
		Stdin:     false,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, scheme.ParameterCodec)

	fmt.Printf("pod %s, container %s\n",podName,containerName)

	var stdout, stderr bytes.Buffer
	err := execute("POST", req.URL(), k.kubeConfig, nil, &stdout, &stderr, false)

	// Split output in lines and loop over skipping first line
	lines := strings.Split(stdout.String(),"\n")

	fmt.Printf("result: \n%s\n",stdout.String())

	cds := []connectionDesc{}

	for _,line := range lines[1:] {
		// Split using regex to match potential multiple space and tabs as separator
		items := regexp.MustCompile("[ \t]+").Split(line,-1)
		if len(items) > 2 {
			// Add connection to the list of Connections for this pod
			strSrcIP := strings.Split(items[2],":")[0]
			strDstIP := strings.Split(items[3],":")[0]
			// Check if we have an IPv6 address. If yes take only the last 8 characters
			if len(strDstIP) > 8  && len(strSrcIP) > 8 {
				strSrcIP = strSrcIP[len(strSrcIP)-8:]
				strDstIP = strDstIP[len(strDstIP)-8:]
			}

			hexSrcIP, _ := strconv.ParseUint(strSrcIP, 16, 32)
			intSrcIP := uint32(hexSrcIP)
			hexDstIP, _ := strconv.ParseUint(strDstIP, 16, 32)
			intDstIP := uint32(hexDstIP)

			srcIP := int2ip(intSrcIP)
			dstIP := int2ip(intDstIP)

			strSrcPort := strings.Split(items[2],":")[1]
			strDstPort := strings.Split(items[3],":")[1]

			srcPort, _ := strconv.ParseUint(strSrcPort, 16, 32)
			dstPort, _ := strconv.ParseUint(strDstPort, 16, 32)

			strHexConnStatus := items[4]
			intConnStatus, _ := strconv.ParseUint(strHexConnStatus, 16, 32)
			var strConnStatus string
			switch intConnStatus {
			case 1:
				strConnStatus = "established"
			case 10:
				strConnStatus = "listening"
			default:
				strConnStatus = fmt.Sprintf("other (code:%d)", intConnStatus)
			}

			cds = append(cds, connectionDesc{protocol, ipversion,srcPort , srcIP.String(), dstPort,dstIP.String(),strConnStatus})
		}
	}
	return cds, err
}

// We use exportable attributes to be able to convert to JSON easily
type podConnections struct {
	Namespace   string
	PodName     string
	Connections []connectionDesc
}

type connectionDesc struct {
	// netns is shared amongst containers of the same pod so there is no need to provide container name
	Protocol string
	IPVersion int
	SrcPort uint64
	SrcIP string
	DstPort uint64
	DstIP string
	// Listen, established ...
	Status string
}

func (k *kubeAPIHandler) GetAllContainersConn() ([]podConnections) {
	pods,_ := k.GetPods()
	var pcs []podConnections
	for _, pod := range pods.Items {
		pc := podConnections{pod.Namespace,pod.Name, []connectionDesc{}}
		for _, container := range pod.Spec.Containers {
			pcTCP,_ := k.GetConn("tcp","tcp",4,pod.Namespace,pod.Name,container.Name)
			pc.Connections = append(pc.Connections, pcTCP...)

			pcTCP6,_ := k.GetConn("tcp6","tcp",6,pod.Namespace,pod.Name,container.Name)
			pc.Connections = append(pc.Connections, pcTCP6...)

			pcUDP,_ := k.GetConn("udp","udp", 4,pod.Namespace,pod.Name,container.Name)
			pc.Connections = append(pc.Connections, pcUDP...)

			pcUDP6,_ := k.GetConn("udp6","udp",6,pod.Namespace,pod.Name,container.Name)
			pc.Connections = append(pc.Connections, pcUDP6...)

			// If we are able to retrieve ports we don't need to go through other containers as they share the same netns
			if len(pcUDP) > 0 || len(pcTCP) > 0 || len(pcUDP6) > 0 || len(pcTCP6) > 0 {
				break
			}
		}
		pcs = append(pcs, pc)
	}
	return pcs
}

func (k *kubeAPIHandler) GetPods() (*v1.PodList, error) {
	return k.clientSet.CoreV1().Pods("").List(metav1.ListOptions{})
}

func (k *kubeAPIHandler) GetNamespaces() (*v1.NamespaceList, error) {
	return k.clientSet.CoreV1().Namespaces().List(metav1.ListOptions{})
}

func (k *kubeAPIHandler) GetNodes() (*v1.NodeList, error) {
	return k.clientSet.CoreV1().Nodes().List(metav1.ListOptions{})
}

func (k *kubeAPIHandler) GetServices() (*v1.ServiceList, error) {
	return k.clientSet.CoreV1().Services("").List(metav1.ListOptions{})
}

func (k *kubeAPIHandler) InitKubeConfig() {
	var err error
	k.kubeConfig,err = getKubeConfig()

	kubernetes.NewForConfig(k.kubeConfig)

	if err != nil {
		panic(err.Error())
	}
}

func (k *kubeAPIHandler) InitClientSet() {
	var err error
	k.clientSet,err = kubernetes.NewForConfig(k.kubeConfig)

	if err != nil {
		panic(err.Error())
	}
}

func (k *kubeAPIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resource := r.URL.Path
	code := http.StatusOK
	response := ""

	switch resource {
	case "pods":
		pods,err := k.GetPods()
		if err != nil {
			panic(err.Error())
		}
		jsonPods,_ := json.Marshal(pods)
		response = string(jsonPods)
	case "namespaces":
		namespaces,err := k.GetNamespaces()
		if err != nil {
			panic(err.Error())
		}
		jsonNamespaces,_ := json.Marshal(namespaces)
		response = string(jsonNamespaces)
	case "nodes":
		nodes,err := k.GetNodes()
		if err != nil {
			panic(err.Error())
		}
		jsonNodes,_ := json.Marshal(nodes)
		response = string(jsonNodes)
	case "connections":
		pcs := k.GetAllContainersConn()

		jsonConn,_ := json.Marshal(pcs)

		response = string(jsonConn)
	case "services":
		services,err := k.GetServices()
		if err != nil {
			panic(err.Error())
		}
		jsonServices, _ := json.Marshal(services)
		response = string(jsonServices)
	default:
		http.Error(w, "Invalid resource", http.StatusNotFound)
		return
	}
	//graph := GenDiagram(pods)
	w.WriteHeader(code)

	fmt.Fprintf(w, response)
}

func kubeAPIServer() http.Handler {
	var k kubeAPIHandler
	k.InitKubeConfig()
	k.InitClientSet()

	return &k
}

func main() {
	// Serve all other files from static directory
	http.Handle("/", http.FileServer(http.Dir("/Users/alex/Documents/Git/kubegraph/kube-graph-ui/build/")))
	http.Handle("/apis/kubernetes/", http.StripPrefix("/apis/kubernetes/",kubeAPIServer()))

	fmt.Printf("Serving on address: http://localhost:9090\n")

	err := http.ListenAndServe(":9090", nil)
	if err != nil {
		panic(err.Error())
	}
}
