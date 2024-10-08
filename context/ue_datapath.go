// Copyright 2019 free5GC.org
//
// SPDX-License-Identifier: Apache-2.0

package context

import (
	"fmt"
	"math"

	"github.com/omec-project/smf/factory"
	"github.com/omec-project/smf/logger"
	"github.com/omec-project/util/idgenerator"
)

type UEPreConfigPaths struct {
	DataPathPool    DataPathPool
	PathIDGenerator *idgenerator.IDGenerator
}

func NewUEDataPathNode(name string) (node *DataPathNode, err error) {
	if smfContext.UserPlaneInformation == nil {
		return nil, fmt.Errorf("smfContext.UserPlaneInformation is nil")
	}
	upNodes := smfContext.UserPlaneInformation.UPNodes

	if _, exist := upNodes[name]; !exist {
		err = fmt.Errorf("upNode %s isn't exist in smfcfg.yaml, but in UERouting.yaml", name)
		return nil, err
	}

	node = &DataPathNode{
		UPF:            upNodes[name].UPF,
		UpLinkTunnel:   &GTPTunnel{},
		DownLinkTunnel: &GTPTunnel{},
	}
	return
}

func NewUEPreConfigPaths(SUPI string, paths []factory.Path) (*UEPreConfigPaths, error) {
	var uePreConfigPaths *UEPreConfigPaths
	ueDataPathPool := NewDataPathPool()
	lowerBound := 0
	pathIDGenerator := idgenerator.NewGenerator(1, math.MaxInt32)

	logger.PduSessLog.Infoln("in NewUEPreConfigPaths")

	for idx, path := range paths {
		dataPath := NewDataPath()

		if idx == 0 {
			dataPath.IsDefaultPath = true
		}

		var pathID int64
		if allocPathID, err := pathIDGenerator.Allocate(); err != nil {
			logger.CtxLog.Warnf("allocate pathID error: %+v", err)
			return nil, err
		} else {
			pathID = allocPathID
		}

		dataPath.Destination.DestinationIP = path.DestinationIP
		dataPath.Destination.DestinationPort = path.DestinationPort
		ueDataPathPool[pathID] = dataPath
		var parentNode *DataPathNode = nil
		for idx, nodeName := range path.UPF {
			newUeNode, err := NewUEDataPathNode(nodeName)
			if err != nil {
				return nil, err
			}

			if idx == lowerBound {
				dataPath.FirstDPNode = newUeNode
			}
			if parentNode != nil {
				newUeNode.AddPrev(parentNode)
				parentNode.AddNext(newUeNode)
			}
			parentNode = newUeNode
		}

		logger.CtxLog.Debugln("new data path added:", dataPath.String())
	}

	uePreConfigPaths = &UEPreConfigPaths{
		DataPathPool:    ueDataPathPool,
		PathIDGenerator: pathIDGenerator,
	}
	return uePreConfigPaths, nil
}

func GetUEPreConfigPaths(SUPI string) *UEPreConfigPaths {
	return smfContext.UEPreConfigPathPool[SUPI]
}

func CheckUEHasPreConfig(SUPI string) (exist bool) {
	_, exist = smfContext.UEPreConfigPathPool[SUPI]
	logger.CtxLog.Infoln("CheckUEHasPreConfig")
	logger.CtxLog.Infoln(smfContext.UEPreConfigPathPool)
	return
}
