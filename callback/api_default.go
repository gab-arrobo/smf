// SPDX-FileCopyrightText: 2021 Open Networking Foundation <info@opennetworking.org>
// Copyright 2019 free5GC.org
//
// SPDX-License-Identifier: Apache-2.0

/*
 * Nsmf_EventExposure
 *
 * Session Management Event Exposure Service API
 *
 * API version: 1.0.0
 * Generated by: OpenAPI Generator (https://openapi-generator.tech)
 */

package callback

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/omec-project/openapi"
	"github.com/omec-project/openapi/models"
	smf_context "github.com/omec-project/smf/context"
	"github.com/omec-project/smf/fsm"
	"github.com/omec-project/smf/logger"
	stats "github.com/omec-project/smf/metrics"
	"github.com/omec-project/smf/msgtypes/svcmsgtypes"
	"github.com/omec-project/smf/transaction"
	"github.com/omec-project/util/httpwrapper"
)

// SubscriptionsPost -
func HTTPSmPolicyUpdateNotification(c *gin.Context) {
	var request models.SmPolicyNotification

	reqBody, err := c.GetRawData()
	if err != nil {
		logger.PduSessLog.Errorf("error: %v", err)
	}

	err = openapi.Deserialize(&request, reqBody, c.ContentType())
	if err != nil {
		logger.PduSessLog.Errorln("deserialize request failed")
	}

	reqWrapper := httpwrapper.NewRequest(c.Request, request)
	reqWrapper.Params["smContextRef"] = c.Params.ByName("smContextRef")

	smContextRef := reqWrapper.Params["smContextRef"]
	logger.PduSessLog.Infof("HTTPSmPolicyUpdateNotification received for UUID = %v", smContextRef)

	txn := transaction.NewTransaction(reqWrapper.Body.(models.SmPolicyNotification), nil, svcmsgtypes.SmPolicyUpdateNotification)
	txn.CtxtKey = smContextRef
	go txn.StartTxnLifeCycle(fsm.SmfTxnFsmHandle)
	<-txn.Status // wait for txn to complete at SMF
	HTTPResponse := txn.Rsp.(*httpwrapper.Response)
	// HTTPResponse := producer.HandleSMPolicyUpdateNotify(smContextRef, reqWrapper.Body.(models.SmPolicyNotification))

	for key, val := range HTTPResponse.Header {
		c.Header(key, val[0])
	}

	resBody, err := openapi.Serialize(HTTPResponse.Body, "application/json")
	if err != nil {
		logger.PduSessLog.Errorln(err)
	}
	_, err = c.Writer.Write(resBody)
	if err != nil {
		logger.PduSessLog.Errorf("error: %v", err)
	}

	c.Status(HTTPResponse.Status)
}

func SmPolicyControlTerminationRequestNotification(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{})
}

func N1N2FailureNotification(c *gin.Context) {
	logger.PduSessLog.Info("receive N1N2 Failure Notification")
	stats.IncrementN11MsgStats(smf_context.SMF_Self().NfInstanceID, string(svcmsgtypes.N1N2MessageTransferFailureNotification), "In", "", "")

	var request models.N1N2MsgTxfrFailureNotification

	req := httpwrapper.NewRequest(c.Request, request)

	req.Params["smContextRef"] = c.Params.ByName("smContextRef")

	smContextRef := req.Params["smContextRef"]
	txn := transaction.NewTransaction(req.Body.(models.N1N2MsgTxfrFailureNotification), nil, svcmsgtypes.N1N2MessageTransferFailureNotification)
	txn.CtxtKey = smContextRef
	go txn.StartTxnLifeCycle(fsm.SmfTxnFsmHandle)
	<-txn.Status

	stats.IncrementN11MsgStats(smf_context.SMF_Self().NfInstanceID, string(svcmsgtypes.N1N2MessageTransferFailureNotification), "Out", http.StatusText(http.StatusNoContent), "")
	c.Status(http.StatusNoContent)
}
