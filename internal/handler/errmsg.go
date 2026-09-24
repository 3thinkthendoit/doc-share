package handler

// 开放平台错误文案：接口 handler 返回与 API 文档页错误码表（apidoc.go openAPIErrors）
// 共用同一份常量，修改文案两处自动同步。仅覆盖 openapi 可达的 handler
// （category / project / doc / openapi），后台专用接口文案不入此表。
const (
	errParam         = "参数错误"
	errTitleEmpty    = "标题不能为空"
	errProjCatBad    = "项目或分类非法"
	errProjRefBad    = "项目非法或无权归属到该项目"
	errCatRefBad     = "分类非法或无权归属到该分类"
	errCatDupCreate  = "创建失败，你的分类名已存在"
	errCatDupUpdate  = "更新失败，你的分类名已存在"
	errCatIDBad      = "分类 id 非法"
	errProjIDBad     = "项目 id 非法"
	errCatForbidden  = "无权操作他人的分类"
	errProjForbidden = "无权操作他人的项目"
	errDocForbidden  = "无权操作他人的文档"
	errCatNotFound   = "分类不存在"
	errProjNotFound  = "项目不存在"
	errDocNotFound   = "文档不存在"
	errCreateFail    = "创建失败"
	errUpdateFail    = "更新失败"
	errDeleteFail    = "删除失败"

	// 签名中间件（openapi.go RequireAppKey）
	errMissingSigHeaders = "缺少签名头：X-App-Key / X-Timestamp / X-Nonce / X-Signature"
	errTimestampBad      = "时间戳无效或与服务器偏差超过 5 分钟"
	errNonceLen          = "nonce 长度需在 8~64 之间"
	errAppKeyBad         = "AppKey 不存在或已禁用"
	errBodyTooLarge      = "读取请求体失败或超过 2MB"
	errSigBad            = "签名校验失败"
	errNonceReplay       = "nonce 已使用，疑似重放请求"
	errOwnerBad          = "密钥属主不存在或已禁用"
)
