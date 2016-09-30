capdir
======

`capdir` is a tool to make [Capistrano](http://capistranorb.com/)-based directory structure.

## Usage

```bash
$ capdir deploy -o /tmp/app --keeps 5 /var/www/app
$ tar xfp /tmp/app.tar | capdir deploy --keeps 5 /var/www/app

$ capdir rollback /var/www/app
```
