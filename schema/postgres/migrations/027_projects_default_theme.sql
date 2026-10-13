-- +goose Up
-- A project's default theme, as a theme reference: '' for none (members see their
-- own choice), a built-in id, or an https URL of a theme file, either optionally
-- ending in #light or #dark. Members' browsers fetch the URL; pug never does.
-- UpdateMetaRequest's rules decide what may be stored. Adding a column with a
-- constant default rewrites nothing.
alter table projects add column default_theme varchar(2048) not null default '';

-- +goose Down
alter table projects drop column default_theme;
