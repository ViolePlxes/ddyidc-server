-- Tencent 慧眼 (faceid) H5 verification joins the existing Alipay channel on the
-- same table. Only a verdict is stored: certify_result holds an AES sealed JSON
-- payload with provider/requestId/errCode/liveStatus/compareStatus/sim/threshold
-- and the decision time. Name, ID number,人脸照片和视频都不落库.
--
-- certify_expires_at bounds the provider BizToken (7200s), so an expired session
-- is reported as "请重新发起" instead of silently failing.
--
-- Additive only; existing rows keep NULL in both columns, so the previous binary
-- keeps working (rollbackCompatible).
ALTER TABLE `customer`.`customer_realname`
  ADD COLUMN `certify_result` MEDIUMTEXT NULL COMMENT '腾讯/支付宝核身判定结果（AES 密文 JSON）',
  ADD COLUMN `certify_expires_at` DATETIME(3) NULL COMMENT '供应商 token 过期时间';
