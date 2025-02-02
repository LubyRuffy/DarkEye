package plugins

import (
	"context"
	"github.com/zsdevX/DarkEye/superscan/dic"
	"gopkg.in/mgo.v2"
	"strings"
	"time"
)

func mongoCheck(s *Service) {
	if mongoUnAuth(s) {
		s.parent.Result.Cracked = Account{Username: "空", Password: "空"}
		s.parent.Result.ServiceName = s.name
		return
	}
	s.crack()
}

func mongodbConn(_ context.Context, s *Service, user, pass string) (ok int) {
	ok = OKNext
	_, err := mgo.DialWithTimeout(
		"mongodb://"+user+":"+pass+"@"+s.parent.TargetIp+":"+s.parent.TargetPort+"/"+"admin",
		time.Duration(Config.TimeOut)*time.Millisecond)
	if err == nil {
		ok = OKDone
	} else {
		if strings.Contains(err.Error(), "Authentication failed") {
			return
		}
		ok = OKStop
	}
	return
}

func mongoUnAuth(s *Service) (ok bool) {
	session, err := mgo.DialWithTimeout(
		s.parent.TargetIp+":"+s.parent.TargetPort,
		time.Duration(Config.TimeOut)*time.Millisecond)
	if err == nil && session.Run("serverStatus", nil) == nil {
		ok = true
	}
	return ok
}

func init() {
	services["mongodb"] = Service{
		name:    "mongodb",
		port:    "27017",
		user:    dic.DIC_USERNAME_MONGODB,
		pass:    dic.DIC_PASSWORD_MONGODB,
		check:   mongoCheck,
		connect: mongodbConn,
		thread:  1,
	}
}
