package modules

import (
	"fmt"
	"os/exec"
	"sync"
	"time"

	cp "elastic/modules/checkpoint"
	global "elastic/modules/global"

	internalapi "k8s.io/cri-api/pkg/apis"
)

func DecisionRepairContainer(resultChan chan global.CheckpointContainer, client internalapi.RuntimeService, systemInfoSet []global.SystemInfo, podIndex map[string]int64,
	podInfoSet []global.PodData, currentRunningPods []string, lenghOfCurrentRunningPods int, priorityMap map[string]global.PriorityContainer,
	removeContainerList []global.CheckpointContainer) {

	var mem int64
	var wg sync.WaitGroup
	var count int
	var repairContainerCandidateList []global.CheckpointContainer

	copyRemoveContainerList := removeContainerList

	if len(copyRemoveContainerList) == 0 {
		return
	}

	// FIFO 구조로 일단 짰으며, 이 부분은 고민이 필요함.
	// 먼저 들어오고 먼저 나가는 방식이 아니라 메모리 조건 만족하면 바로 나갈 수 있게끔??00000
	// 연산 cost가 너무 커짐... 메모리 순으로 다시 sort해야 한다.
	// 아니면 우선순위가 높은 순으로 정렬?? 이 경우에는 다른 작업이 못나갈 가능성이 있음....

	// 현재 실행중인 파드의 메모리 limit 합을 더한 것
	/*
		var sumLimitMemorySize int64

		for _, podName := range currentRunningPods {
			pod := podInfoSet[podIndex[podName]]
			for _, container := range pod.Container {
				res := container.Resource

				// exception handling
				if len(res) == 0 {
					continue
				}
				sumLimitMemorySize += container.Cgroup.MemoryLimitInBytes
			}
		}*/

	sumLimitMemorySize := int64(systemInfoSet[len(systemInfoSet)-1].Memory.Used)

	for {
		if len(copyRemoveContainerList) == 0 {
			break
		}
		mem += int64(float64(copyRemoveContainerList[0].CheckpointData.MemoryLimitInBytes) * 1.1)

		if mem+sumLimitMemorySize > int64(float64(systemInfoSet[len(systemInfoSet)-1].Memory.Total)*global.MAX_REPAIR_MEMORY_USAGE_THRESHOLD) {
			break
		}
		repairContainerCandidateList = append(repairContainerCandidateList, copyRemoveContainerList[0])
		resultChan <- copyRemoveContainerList[0]
		if len(copyRemoveContainerList) == 1 {
			break
		}
		copyRemoveContainerList = copyRemoveContainerList[1 : len(copyRemoveContainerList)-1]
	}

	for _, container := range repairContainerCandidateList {
		if (container.DuringCreateContainer == false) && (container.CreateContainer == false) {
			count++
		}
	}

	wg.Add(count)
	for _, repairContainerCandidate := range repairContainerCandidateList {
		if repairContainerCandidate.DuringCreateContainer || repairContainerCandidate.CreateContainer {
			continue
		}
		// 비동기 구현
		go func(container global.CheckpointContainer) {
			container.DuringCreateContainer = true
			resultChan <- container

			container.StartRepairTime = time.Now().Unix()
			for {
				if cp.MakeContainerFromCheckpoint(container) {
					break
				} else {
					time.Sleep(time.Second)
				}
			}

			container.DuringCreateContainer = false
			container.CreatingContainer = true
			resultChan <- container

			repairRequestMemory := int64(float64(container.CheckpointData.MemoryLimitInBytes) * 1.1) // 실제 할당되어야 하는 메모리 크기...
			// 그렇다고 이걸 컨테이너 명령어로 할 수 없음. 이는 request이상을 받지 못함을 보장함

			repairContainerCandidate.CheckpointData.RemoveEndTime = time.Now().Unix()
			RestoreContainer(container)
			podInfoSet = UpdatePodData(client, container, podIndex, podInfoSet, repairRequestMemory)

			container.CreatingContainer = false
			container.CreateContainer = true

			container.EndRepairTime = time.Now().Unix()

			resultChan <- container

		}(repairContainerCandidate)
	}
	wg.Wait()
}

func RestoreContainer(container global.CheckpointContainer) {
	// kubernetes master에 연결해서 명령어 보내야 할듯....

	command := fmt.Sprintf(`kubectl create -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: %s
spec:
  restartPolicy: OnFailure
  containers:
  - name: %s
    image: localhost/%s:latest
    resources:
      requests:
        memory: %d
      limits:
        cpu: %f
        memory: %d
  nodeName: %s
EOF`, container.PodName, container.ContainerName, container.PodName, global.MIN_SIZE_PER_CONTAINER,
		float64(global.DEFAULT_CPU_QUOTA)*0.00001, global.MAX_SIZE_PER_CONTAINER, global.NODENAME)

	fmt.Println(command)

	//command1 := "kubectl create -f - <<EOF\napiVersion: v1\nkind: Pod\nmetadata:\n  name: " + container.PodName + "\n"
	//command2 := "spec:\n  containers:\n  - name: " + container.ContainerName + "\n    image: localhost/" + container.PodName + ":latest\n    "
	//command3 := "resources:\n      requests:\n        memory: " + strconv.FormatInt(global.MIN_SIZE_PER_CONTAINER, 10)
	//command4 := "\n      limits:\n        cpu: " + strconv.FormatFloat(float64(global.DEFAULT_CPU_QUOTA)*0.00001, 'f', -1, 64) + "\n        memory: " + strconv.FormatInt(memoryLimits, 10) + "\n  "
	//command5 := "nodeName: " + global.NODENAME + "\nEOF" // 프로그램마다 노드네임 다르게 설정해야 한다.
	//fmt.Println(command1 + command2 + command3 + command4 + command5)
	_, err := exec.Command("bash", "-c", command).Output()
	if err != nil {
		fmt.Println(err)
	}
}
