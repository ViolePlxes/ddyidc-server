-- The sidebar "大类" becomes editor-owned data like the routes themselves:
-- administrators add, rename, reorder and delete categories from the navigation
-- manager instead of a hard-coded list.
CREATE TABLE IF NOT EXISTS system.navigation_categories (
 id BIGINT PRIMARY KEY,
 name VARCHAR(32) NOT NULL,
 sort INT NOT NULL DEFAULT 0,
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY navigation_category_name (name)
) ENGINE=InnoDB;

INSERT IGNORE INTO system.navigation_categories(id,name,sort,created_at,updated_at) VALUES
 (960000001,'业务管理',0,NOW(3),NOW(3)),
 (960000002,'服务管理',1,NOW(3),NOW(3)),
 (960000003,'支持与设置',2,NOW(3),NOW(3)),
 (960000004,'账户与权益',3,NOW(3),NOW(3)),
 (960000005,'运营管理',4,NOW(3),NOW(3)),
 (960000006,'销售管理',5,NOW(3),NOW(3)),
 (960000007,'更多服务',6,NOW(3),NOW(3));
