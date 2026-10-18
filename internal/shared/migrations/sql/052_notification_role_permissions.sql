-- Reuse the platform's active admin roles; configuration stays super-admin only.
INSERT INTO system.permissions(id,code,name,module) VALUES
 (800000000005201,'notification:view','查看站内消息任务','notification'),
 (800000000005202,'notification:create','创建站内消息群发','notification'),
 (800000000005203,'notification:retry','重试站内消息群发','notification'),
 (800000000005204,'notification:cancel','取消站内消息群发','notification')
 ON DUPLICATE KEY UPDATE id=id;
