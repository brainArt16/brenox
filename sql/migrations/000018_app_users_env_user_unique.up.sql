ALTER TABLE app_users
    DROP CONSTRAINT IF EXISTS app_users_app_id_user_id_key;

ALTER TABLE app_users
    ADD CONSTRAINT app_users_app_id_user_id_environment_key
    UNIQUE (app_id, user_id, environment);
