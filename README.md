capdir
======

`capdir` is a tool to make [Capistrano](http://capistranorb.com/)-based directory structure.

## Usage

```bash
$ capdir --keeps 5 /tmp/app /var/www/app
$ cat /tmp/app.tar | capdir --keeps 5 /var/www/app

$ capdir --rollback /var/www/app
```
