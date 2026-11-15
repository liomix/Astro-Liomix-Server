-- 最小权限：应用账号与迁移账号分开。
--
-- 为什么分开：迁移需要 DDL（ALTER TABLE 要求对象 owner 权限），应用不需要。
-- 合成一个账号，等于「应用被攻破」= 「数据库结构可被改写」，而这个账号天天
-- 暴露在公网请求上。
--
-- 用法（密码走 psql 变量，绝不写进这个文件）：
--
--   psql -U postgres -d Blog \
--        -v app_password="$(openssl rand -hex 24)" \
--        -v migrator_password="$(openssl rand -hex 24)" \
--        -f configs/roles.sql
--
-- 记下两个密码：应用账号的写进 pgpass / 密钥管理，迁移账号的只给部署流水线。
-- 脚本是幂等的：角色已存在就跳过创建，授权与所有权每次重设一遍。
--
-- 之后把应用连的 DATABASE_URL 换成：
--   postgres://blog_app@localhost:5432/Blog?sslmode=disable
-- （密码留给 pgpass；生产环境用 sslmode=verify-full）

\set ON_ERROR_STOP on

-- 1. 建角色。psql 的变量在 DO 块里不展开，所以用 \gexec 把语句拼出来执行。
SELECT format('CREATE ROLE blog_migrator LOGIN PASSWORD %L', :'migrator_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'blog_migrator')
\gexec

SELECT format('CREATE ROLE blog_app LOGIN PASSWORD %L', :'app_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'blog_app')
\gexec

-- 两个角色都不继承别的角色的权限：拿到密码不等于顺带拿到别人的权限。
ALTER ROLE blog_app NOINHERIT;
ALTER ROLE blog_migrator NOINHERIT;

-- 2. 连库权限。库名从 current_database() 取，脚本不绑定某一个库名。
SELECT format('GRANT CONNECT ON DATABASE %I TO blog_app, blog_migrator', current_database())
\gexec

GRANT USAGE ON SCHEMA public TO blog_app, blog_migrator;
-- 建对象（迁移用）。应用只需要 USAGE，不需要 CREATE。
GRANT CREATE ON SCHEMA public TO blog_migrator;

-- 3. 把现有对象的 owner 交给迁移账号。
--    没有这一步，blog_migrator 无法 ALTER 已经存在的表——owner 才能改结构。
SELECT format('ALTER TABLE %I.%I OWNER TO blog_migrator', n.nspname, c.relname)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'S', 'v', 'm')
\gexec

-- 4. 应用只拿 DML。注意没有 TRUNCATE、没有 REFERENCES、没有 TRIGGER。
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO blog_app;
-- 序列的 USAGE 是 nextval 的前提；ALTER 不需要。
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO blog_app;

-- 5. 以后新建的对象自动按同样的规则授权。
--    漏了这一句，下一次迁移建的新表应用就没权限，而这个错误只在部署后才出现。
ALTER DEFAULT PRIVILEGES FOR ROLE blog_migrator IN SCHEMA public
	GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO blog_app;
ALTER DEFAULT PRIVILEGES FOR ROLE blog_migrator IN SCHEMA public
	GRANT USAGE, SELECT ON SEQUENCES TO blog_app;

-- 6. 迁移账号自己也要能写 schema_migrations（它就是一张普通表）。
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO blog_migrator;

-- 7. 自检：把两个角色实际拿到的权限打出来，执行完扫一眼。
SELECT grantee, privilege_type
FROM information_schema.table_privileges
WHERE grantee IN ('blog_app', 'blog_migrator')
ORDER BY grantee, privilege_type;

SELECT rolname, rolcanlogin, rolsuper, rolcreatedb, rolcreaterole
FROM pg_roles
WHERE rolname IN ('blog_app', 'blog_migrator');
