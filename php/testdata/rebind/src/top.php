<?php

use Shop\Cart;

$g = new Cart();
rebindG();
$g->add('global');

$q = new Cart();
rebindQ();
$q->add('globals');
