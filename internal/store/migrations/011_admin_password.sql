-- A blank admin password matched a blank login and granted admin to any node in range; this sets it to the firmware's default.

UPDATE repeater SET admin_password = 'password' WHERE admin_password = '';
