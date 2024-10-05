// Copyright 2019 free5GC.org
//
// SPDX-License-Identifier: Apache-2.0

/*
 * Nsmf_PDUSession
 *
 * SMF PDU Session Service
 *
 * API version: 1.0.0
 * Generated by: OpenAPI Generator (https://openapi-generator.tech)
 */

package pdusession

import (
	"net/http"
	"strings"

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
	mi "github.com/omec-project/util/metricinfo"
)

// HTTPPostSmContexts - Create SM Context
func HTTPPostSmContexts(c *gin.Context) {
	logger.PduSessLog.Infoln("receive create SM Context Request")
	var request models.PostSmContextsRequest
	stats.IncrementN11MsgStats(smf_context.SMF_Self().NfInstanceID, string(svcmsgtypes.CreateSmContext), "In", "", "")
	stats.PublishMsgEvent(mi.Smf_msg_type_pdu_sess_create_req)

	request.JsonData = new(models.SmContextCreateData)

	s := strings.Split(c.GetHeader("Content-Type"), ";")
	var err error
	switch s[0] {
	case "application/json":
		err = c.ShouldBindJSON(request.JsonData)
	case "multipart/related":
		err = c.ShouldBindWith(&request, openapi.MultipartRelatedBinding{})
	}

	if err != nil {
		problemDetail := "[Request Body] " + err.Error()
		rsp := models.ProblemDetails{
			Title:  "Malformed request syntax",
			Status: http.StatusBadRequest,
			Detail: problemDetail,
		}
		stats.IncrementN11MsgStats(smf_context.SMF_Self().NfInstanceID, string(svcmsgtypes.CreateSmContext), "Out", http.StatusText(http.StatusBadRequest), "Malformed")
		logger.PduSessLog.Errorln(problemDetail)
		c.JSON(http.StatusBadRequest, rsp)
		return
	}

	req := httpwrapper.NewRequest(c.Request, request)
	txn := transaction.NewTransaction(req.Body.(models.PostSmContextsRequest), nil, svcmsgtypes.SmfMsgType(svcmsgtypes.CreateSmContext))

	go txn.StartTxnLifeCycle(fsm.SmfTxnFsmHandle)
	<-txn.Status // wait for txn to complete at SMF
	HTTPResponse := txn.Rsp.(*httpwrapper.Response)
	smContext := txn.Ctxt.(*smf_context.SMContext)
	errStr := ""
	if txn.Err != nil {
		errStr = txn.Err.Error()
	}

	// Http Response to AMF

	for key, val := range HTTPResponse.Header {
		c.Header(key, val[0])
	}
	stats.IncrementN11MsgStats(smf_context.SMF_Self().NfInstanceID, string(svcmsgtypes.CreateSmContext), "Out", http.StatusText(HTTPResponse.Status), errStr)
	switch HTTPResponse.Status {
	case http.StatusCreated,
		http.StatusBadRequest,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		c.Render(HTTPResponse.Status, openapi.MultipartRelatedRender{Data: HTTPResponse.Body})
	default:
		c.JSON(HTTPResponse.Status, HTTPResponse.Body)
	}

	go func(smContext *smf_context.SMContext) {
		var txn *transaction.Transaction
		if HTTPResponse.Status == http.StatusCreated {
			txn = transaction.NewTransaction(nil, nil, svcmsgtypes.SmfMsgType(svcmsgtypes.PfcpSessCreate))
			txn.Ctxt = smContext
			go txn.StartTxnLifeCycle(fsm.SmfTxnFsmHandle)
			<-txn.Status
		} else {
			smf_context.RemoveSMContext(smContext.Ref)
		}
	}(smContext)
}
