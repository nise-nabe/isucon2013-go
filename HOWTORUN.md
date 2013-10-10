ami-b34ad3b2

isucon ユーザで入るための初期ログイン設定
$ sudo su -
# cp /home/ec2-user/.ssh/authorized_keys /home/isucon/.ssh/authorized_keys
# chown isucon:isucon /home/isucon/.ssh/authorized_keys
# exit
$ exit

isucon ユーザでログインしなおし

アプリ使用設定
$ cd webapp
$ rm -rf go
$ git clone https://github.com/nise-nabe/isucon2013-go.git go
$ cd go
$ git checkout after
$ go get github.com/knieriem/markdown
$ go build -o app

DB 設定
$ sudo cp /home/isucon/webapp/go/init/my.cnf /usr/my.cnf
$ sudo /etc/init.d/mysql restart

起動設定
$ sudo vim /etc/supervisord.conf 
$ sudo supervisorctl stop isucon_perl
$ sudo supervisorctl start isucon_go

起動
$ sudo isucon3 benchmark --workload=3 --init=/home/isucon/webapp/go/init/init.sh
