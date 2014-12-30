/*
The MIT License (MIT)

Copyright (c) 2013-2014 winlin

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
the Software, and to permit persons to whom the Software is furnished to do so,
subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
*/

package rtmp

import (
    "github.com/winlinvip/go-srs/protocol"
    "errors"
    "net"
    "github.com/winlinvip/go-srs/core"
)

var FinalStage = errors.New("rtmp final stage")

/**
* Helper functions for stage.
 */
func identifyIgnoreMessage(msg *protocol.RtmpMessage, logger core.Logger) bool {
    h := &msg.Header

    if h.IsAckledgement() || h.IsSetChunkSize() || h.IsWindowAckledgementSize() || h.IsUserControlMessage() {
        logger.Info("ignore the ack/setChunkSize/windowAck/userControl msg")
        return true
    }

    if !h.IsAmf0Command() && !h.IsAmf3Command() {
        logger.Trace("identify ignore messages except AMF0/AMF3 command message, type=%d", h.MessageType)
        return true
    }

    return false
}

/**
* the first stage, connect vhost/app.
* @remark this stage only enter one time.
 */
type connectStage struct {
    conn *protocol.Conn
}

func (stage *connectStage) ConsumeMessage(msg *protocol.RtmpMessage) (err error) {
    logger := stage.conn.Logger

    // always expect the connect app message.
    if !msg.Header.IsAmf0Command() && !msg.Header.IsAmf3Command() {
        return
    }

    var pkt protocol.RtmpPacket
    if pkt,err = stage.conn.Protocol.DecodeMessage(msg); err != nil {
        return
    }

    // got connect app packet
    if pkt,ok := pkt.(*protocol.RtmpConnectAppPacket); ok {
        req := &stage.conn.Request
        if err = req.Parse(pkt.CommandObject, pkt.Arguments, logger); err != nil {
            logger.Error("parse request from connect app packet failed.")
            return
        }
        logger.Info("rtmp connect app success")

        // discovery vhost, resolve the vhost from config
        // TODO: FIXME: implements it

        // check the request paramaters.
        if err = req.Validate(logger); err != nil {
            return
        }
        logger.Info("discovery app success. schema=%v, vhost=%v, port=%v, app=%v",
            req.Schema, req.Vhost, req.Port, req.App)

        // check vhost
        // TODO: FIXME: implements it
        logger.Info("check vhost success.")

        logger.Trace("connect app, tcUrl=%v, pageUrl=%v, swfUrl=%v, schema=%v, vhost=%v, port=%v, app=%v, args=%v",
            req.TcUrl, req.PageUrl, req.SwfUrl, req.Schema, req.Vhost, req.Port, req.App, req.FormatArgs())

        // show client identity
        si := SrsInfo{}
        si.Parse(req.Args)
        if si.SrsPid > 0 {
            logger.Trace("edge-srs ip=%v, version=%v, pid=%v, id=%v",
                si.SrsServerIp, si.SrsVersion, si.SrsPid, si.SrsId)
        }

        if err = stage.conn.SetWindowAckSize(2.5 * 1000 * 1000); err != nil {
            logger.Error("set window acknowledgement size failed.")
            return
        }
        logger.Info("set window acknowledgement size success")

        if err = stage.conn.SetPeerBandwidth(2.5 * 1000 * 1000, 2); err != nil {
            logger.Error("set peer bandwidth failed.")
            return
        }
        logger.Info("set peer bandwidth success")

        // get the ip which client connected.
        var iorw *net.TCPConn = stage.conn.IoRw
        localIp := iorw.LocalAddr().String()

        // do bandwidth test if connect to the vhost which is for bandwidth check.
        // TODO: FIXME: implements it

        // do token traverse before serve it.
        // @see https://github.com/winlinvip/simple-rtmp-server/pull/239
        // TODO: FIXME: implements it

        // response the client connect ok.
        if err = stage.conn.ResponseConnectApp(req.ObjectEncoding, localIp); err != nil {
            logger.Error("response connect app failed.")
            return
        }
        logger.Info("response connect app success")

        if err = stage.conn.OnBwDone(); err != nil {
            logger.Error("on_bw_done failed.")
            return
        }
        logger.Info("on_bw_done success")

        // use next stage.
        stage.conn.Stage = &identifyClientStage{conn:stage.conn,}
        return
    }

    return
}

/**
* the second stage, identify the client.
* @remark this stage can re-enter
 */
type identifyClientStage struct {
    conn *protocol.Conn
}

func (stage *identifyClientStage) ConsumeMessage(msg *protocol.RtmpMessage) (err error) {
    logger := stage.conn.Logger

    if identifyIgnoreMessage(msg, logger) {
        return
    }

    var pkt protocol.RtmpPacket
    if pkt,err = stage.conn.Protocol.DecodeMessage(msg); err != nil {
        logger.Error("identify decode message failed")
        return
    }

    switch pkt := pkt.(type) {
    case *protocol.RtmpCreateStreamPacket:
        logger.Info("identify client by create stream, play or flash publish.")
        stage.conn.StreamId++
        if err = stage.conn.ResponseCreateStream(float64(pkt.TransactionId), stage.conn.StreamId); err != nil {
            logger.Error("send createStream response message failed.")
            return
        }
        logger.Info("send createStream response message success.")

        // use next stage.
        stage.conn.Stage = &identifyClientCreateStreamStage{conn: stage.conn,}
        return
    case *protocol.RtmpPlayPacket:
        logger.Info("level0 identify client by play.")
        logger.Info("identity client type=play, stream_name=%v, duration=%.2f", pkt.StreamName, pkt.Duration)

        // use next stage.
        stage.conn.Stage = &playStage{
            conn: stage.conn,
            streamName: string(pkt.StreamName),
            duration: float64(pkt.Duration),
        }
        return
    case *protocol.RtmpReleaseStreamPacket:
        logger.Info("identify client by releaseStream, fmle publish.")
        if err = stage.conn.ResponseReleaseStream(float64(pkt.TransactionId)); err != nil {
            logger.Error("send releaseStream response message failed.")
            return
        }
        logger.Info("send releaseStream response message success.")

        // use next stage.
        stage.conn.Stage = &fmlePublishStage{
            conn: stage.conn,
            streamName: string(pkt.StreamName),
        }
        return
    case *protocol.RtmpCallPacket:
        // call msg,
        // support response null first,
        // @see https://github.com/winlinvip/simple-rtmp-server/issues/106
        // TODO: FIXME: response in right way, or forward in edge mode.
        return
    default:
        logger.Trace("ignore AMF0/AMF3 command message.")
    }

    // use next stage.
    stage.conn.Stage = &finalStage{}
    return
}

/**
* the sub stage of identify client, the create stream stage, maybe publish or play.
* @remark this stage can re-enter
 */
type identifyClientCreateStreamStage struct {
    conn *protocol.Conn
}

func (stage *identifyClientCreateStreamStage) ConsumeMessage(msg *protocol.RtmpMessage) (err error) {
    logger := stage.conn.Logger

    if identifyIgnoreMessage(msg, logger) {
        return
    }

    var pkt protocol.RtmpPacket
    if pkt,err = stage.conn.Protocol.DecodeMessage(msg); err != nil {
        logger.Error("identify decode message failed")
        return
    }

    switch pkt := pkt.(type) {
    case *protocol.RtmpPlayPacket:
        logger.Info("identity client type=play, stream_name=%v, duration=%.2f", pkt.StreamName, pkt.Duration)

        // use next stage.
        stage.conn.Stage = &playStage{
            conn: stage.conn,
            streamName: string(pkt.StreamName),
            duration: float64(pkt.Duration),
        }
        return
    case *protocol.RtmpPublishPacket:
        logger.Info("identify client by publish, falsh publish.")

        // use next stage.
        stage.conn.Stage = &flashPublishStage{
            conn: stage.conn,
            streamName: string(pkt.StreamName),
        }
        return
    case *protocol.RtmpCreateStreamPacket:
        logger.Info("identify client by create stream, play or flash publish.")
        return
    }

    return
}

/**
* the stage of service client as playing stream.
* @remark this stage can re-enter
 */
type playStage struct {
    conn *protocol.Conn
    streamName string
    duration float64
}

func (stage *playStage) ConsumeMessage(msg *protocol.RtmpMessage) (err error) {
    logger := stage.conn.Logger
    logger.Trace("client identified, type=Play, stream_name=%s, duration=%.2f", stage.streamName, stage.duration)

    // set chunk size to larger.
    // TODO: FIXME: implements it.

    // TODO: FIXME: implements it.
    return
}

/**
* the stage of service client as FMLE publishing stream.
* @remark this stage can re-enter
 */
type fmlePublishStage struct {
    conn *protocol.Conn
    streamName string
}

func (stage *fmlePublishStage) ConsumeMessage(msg *protocol.RtmpMessage) (err error) {
    logger := stage.conn.Logger
    req := &stage.conn.Request
    logger.Trace("client identified, type=publish(FMLEPublish), stream_name=%s", stage.streamName)

    // set chunk size to larger.
    // TODO: FIXME: implements it.

    // find a source to serve.
    var source *RtmpSource
    if source,err = FindSource(req, logger); err != nil {
        return
    }
    core.AssertNotNil(source)

    // check ASAP, to fail it faster if invalid.
    // TODO: FIXME: implements it.

    enabledCache := false
    vhostIsEdge := false
    logger.Trace("source url=%s, ip=%s, cache=%v, is_edge=%v, source_id=%d[%d]",
        req.StreamUrl(), stage.conn.IoRw.RemoteAddr().String(), enabledCache, vhostIsEdge, source.SrsId, source.SrsId)
    source.GopCache(enabledCache)

    // source->on_edge_start_publish
    // TODO: FIXME: implements it.

    stage.conn.Stage = &fmlePublishStartStage{
        conn: stage.conn,
        source: source,
    }
    // current stage is merged into next stage.
    return stage.conn.Stage.ConsumeMessage(msg)
}

/**
* fmle start publish stage
 */
type fmlePublishStartStage struct {
    conn *protocol.Conn
    source *RtmpSource
}

func (stage *fmlePublishStartStage) ConsumeMessage(msg *protocol.RtmpMessage) (err error) {
    logger := stage.conn.Logger

    var pkt protocol.RtmpPacket
    if pkt,err = stage.conn.Protocol.DecodeMessage(msg); err != nil {
        logger.Error("identify decode message failed")
        return
    }

    switch pkt := pkt.(type) {
    case *protocol.RtmpFcPublishPacket:
        // FCPublish
        if err = stage.conn.ResponseFcPublish(float64(pkt.TransactionId)); err != nil {
            logger.Error("send FCPublish response message failed.")
            return
        }
        logger.Info("send FCPublish response message success.")
    case *protocol.RtmpCreateStreamPacket:
        // createStream
        stage.conn.StreamId++
        if err = stage.conn.ResponseCreateStream(float64(pkt.TransactionId), stage.conn.StreamId); err != nil {
            logger.Error("send createStream response message failed.")
            return
        }
        logger.Info("send createStream response message success.")
    case *protocol.RtmpPublishPacket:
        // publish
        // publish response onFCPublish(NetStream.Publish.Start)
        if err = stage.conn.ResponsePublish(float64(pkt.TransactionId), stage.conn.StreamId); err != nil {
            logger.Error("send onFCPublish(NetStream.Publish.Start) message failed")
            return
        }
        logger.Info("send onFCPublish(NetStream.Publish.Start) message success.")
        // publish response onStatus(NetStream.Publish.Start)
        if err = stage.conn.OnStatus(stage.conn.StreamId); err != nil {
            logger.Error("send onStatus(NetStream.Publish.Start) message failed")
            return
        }
        logger.Info("send onStatus(NetStream.Publish.Start) message success.")

        logger.Info("start to publish stream success")
        stage.conn.Stage = &fmlePublishingStage{
            conn: stage.conn,
            source: stage.source,
        }
    default:
        logger.Info("fmle publish start stage ignore msg %v", msg)
    }

    return
}

/**
* fmle publishing stage
 */
type fmlePublishingStage struct {
    conn *protocol.Conn
    source *RtmpSource
}

func (stage *fmlePublishingStage) ConsumeMessage(msg *protocol.RtmpMessage) (err error) {
    logger := stage.conn.Logger

    // process publish event.
    if msg.Header.IsAmf3Command() || msg.Header.IsAmf0Command() {
        // for fmle, drop others except the fmle start packet.
        var pkt protocol.RtmpPacket
        if pkt,err = stage.conn.Protocol.DecodeMessage(msg); err != nil {
            logger.Error("fmle decode unpublish message failed")
            return
        }

        if _,ok := pkt.(*protocol.RtmpFcUnPublishPacket); ok {
            // TOOD: FIMXE: implements it.
            return protocol.RtmpControlRepublish
        }
        logger.Trace("fmle ignore AMF0/AMF3 command message.")
        return
    }

    // video, audio, data message
    return stage.source.OnMessage(msg)
}

/**
* the stage of service client as flash publishing stream.
* @remark this stage can re-enter
 */
type flashPublishStage struct {
    conn *protocol.Conn
    streamName string
}

func (stage *flashPublishStage) ConsumeMessage(msg *protocol.RtmpMessage) (err error) {
    logger := stage.conn.Logger
    logger.Trace("client identified, type=publish(FlashPublish), stream_name=%s", stage.streamName)

    // set chunk size to larger.
    // TODO: FIXME: implements it.

    // TODO: FIXME: implements it.
    return
}

/**
* the last stage close connection.
 */
type finalStage struct {
}

func (stage *finalStage) ConsumeMessage(msg *protocol.RtmpMessage) (err error) {
    return FinalStage
}
