-- +goose Up
-- '' is no default; UpdateMetaRequest.default_theme's rules decide what else may be
-- stored. A constant default adds the column without rewriting the table.
alter table projects add column default_theme varchar(2048) not null default '';

-- +goose Down
alter table projects drop column default_theme;
