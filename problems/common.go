/**
 * Created with IntelliJ IDEA.
 * User: Georg
 * Date: 21.10.13
 * Time: 13:31
 * To change this template use File | Settings | File Templates.
 */
package problems

type Problem interface {
	Initialize() []float64
	Fcn(t float64, yT []float64, dy_out []float64)
}
