caplize
======

`caplize` is a tool to make [Capistrano](http://capistranorb.com/)-based directory structure.

## Usage

```bash
$ caplize --keeps 5 /tmp/app /var/www/app
$ cat /tmp/app.tar | caplize --keeps 5 /var/www/app

$ caplize --rollback /var/www/app
```
