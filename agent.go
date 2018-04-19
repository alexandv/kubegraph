package main

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"os"
	"strconv"
	"strings"
)

type Process struct {
	pid   int
	Connections	[]connectionDesc
}

type container struct {
	Id	string
	PidNS	int64
	NetNS	int64
	Processes	[]Process
}

func getNamespaceId(pid int,nsType string) int64 {
	nsFileStr := fmt.Sprintf("/procmnt/%d/ns/%s",pid,nsType)

	nsFileLink,err := os.Readlink(nsFileStr)
	if err != nil {
		fmt.Printf("Error %s",err)
	}
	nsId := nsFileLink[5:]
	nsId = nsId[:len(nsId)-1]

	id,_ := strconv.ParseInt(nsId,10,64)
	return id
}

func processes() (map[int64][]*Process, error) {
	d, err := os.Open("/procmnt")
	if err != nil {
		return nil, err
	}
	defer d.Close()

	m := make(map[int64][]*Process)

	for {
		fis, err := d.Readdir(10)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}


		for _, fi := range fis {
			// We only care about directories, since all pids are dirs
			if !fi.IsDir() {
				continue
			}

			// We only care if the name starts with a numeric
			name := fi.Name()
			if name[0] < '0' || name[0] > '9' {
				continue
			}

			// From this point forward, any errors we just ignore, because
			// it might simply be that the process doesn't exist anymore.
			pid, err := strconv.ParseInt(name, 10, 0)
			if err != nil {
				continue
			}

			p := Process{pid:int(pid)}
			if err != nil {
				continue
			}

			pidNS := getNamespaceId(pid,"pid")
			netNS := getNamespaceId(pid,"net")
			p.pidns = pidNS
			p.netns = netNS

			m[pidNS] = append(m[pidNS],&p)
		}
	}

	return m, nil
}

func findConnections(pid int) ([]Connection, error) {
	fdPath := fmt.Sprintf("/procmnt/%d/fd",pid)
	d, err := os.Open(fdPath)
	if err != nil {
		return nil, err
	}
	defer d.Close()

	for {
		fis, err := d.Readdir(10)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		for _, fi := range fis {
			link,_ := os.Readlink(fi.Name())

			if strings.HasPrefix(link,"socket:") {

			}
		}
	}
}

func main() {
	cli, err := client.NewClient("unix:///varmnt/run/docker.sock","1.35",nil,nil)
	if err != nil {
		panic(err)
	}

	containers, err := cli.ContainerList(context.Background(), types.ContainerListOptions{All:true})
	if err != nil {
		panic(err)
	}

	for _, container := range containers {
		ins,_ := cli.ContainerInspect(context.Background(),container.ID)

		proc := Process{pid:ins.State.Pid}
		pidNS := getNamespaceId(ins.State.Pid,"pid")
		netNS := getNamespaceId(ins.State.Pid,"net")
		findConnections(ins.State.Pid)
		cont := container{Id:container.ID,NetNS:netNS,PidNS:pidNS,Processes:[]Process{proc}}

		fmt.Printf("Container Id:%s Image:%s Pid:%d\n", container.ID, container.Image,ins.State.Pid)
	}
	processes()
}
