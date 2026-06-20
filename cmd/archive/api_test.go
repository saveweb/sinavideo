package main

import (
	"encoding/json"
	"testing"
)

var getPlayInfoRaw = []byte(`{"Message":"OK","code":1,"data":{"approve_status":"1","channel_path":"","create_time":"1215872586","delogo":"0","description":"“2008年惠州华轩杯全国象棋甲级联赛 ”申鹏VS陈寒峰【中炮对左炮封车转半途列炮】。张强、郭莉萍主讲，欢迎收看！","drm":{"pub_key":"","ver":""},"embed_swf":"","filter_ip":0,"from":"shijiao","gif":"","holovideo":"0","image":"https://p3.ivideo.sina.com.cn/video/246/318/416/246318416.jpg","is_drm":0,"length":"1315733","logo":"0","media_id":"","media_tag":"246318416|0|0|0|0,0|0|0|0|0|0","position":"0","ratio":"1.333","recommend_image":"https://p.ivideo.sina.com.cn/video/246/318/416/246318416.jpg","status":"1","tags":"","title":"申鹏VS陈寒峰【象棋世界08-07-10】","transcode_image":"https://p.ivideo.sina.com.cn/video/246/318/416/246318416.jpg","transcode_status":"1","transcode_system":"colonel","video_fp":"","video_id":"246318416","video_sc":"","video_url":"","videos":[{"codec":"H264","definition":"sd","file_id":"14926796","height":"0","length":"1315733","md5":"","size":"42260214","status":"0","type":"flv","width":"0","avc":"","dispatch_result":{"result":"ok","url":"http://edge.ivideo.sina.com.cn/14926796.flv?KID=sina,viask\u0026Expires=1782144000\u0026ssig=8kcyC4ViYa\u0026reqid=","url_from":"vms","bakurl":"http://api.ivideo.sina.com.cn/public/video/play/url?appname=sinaplayer_pc\u0026appver=V11220.210521.03\u0026applt=web\u0026tags=sinaplayer_pc\u0026video_id=246318416\u0026vid=14926796\u0026direct=1\u0026report=0"}}],"wb_video_fp":"","wb_video_fp_iid":""},"error":"","errorMessage":"OK","meta":{"latest_version":"20220526-1","obsoleted":0,"offline":0,"server_utc_ms":1781982300475,"this_version":"20220526-1"}}`)

func TestGetPlayInfo(t *testing.T) {
	var p PlayResp
	if err := json.Unmarshal(getPlayInfoRaw, &p); err != nil {
		t.Fatal(err)
		return
	}
	switch p.Code {
	case 0:
		t.Errorf("play api error, expected code 1, got %d, message: %s", p.Code, p.Message)
	case 1:
		var data PlayData
		if err := json.Unmarshal(p.Data, &data); err != nil {
			t.Errorf("unmarshal play data: %v", err)
			return
		}
		t.Logf("play info: %v", data)
	default:
		t.Errorf("unexpected code: %d, message: %s", p.Code, p.Message)
	}

}
